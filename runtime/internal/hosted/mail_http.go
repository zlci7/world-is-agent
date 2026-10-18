package hosted

import (
	"errors"
	"fmt"
	"log"
	"mime"
	"net/http"
	"strings"

	"gameagent/runtime/internal/accounts"
)

func mimeSubject(value string) string { return mime.QEncoding.Encode("utf-8", value) }

func (s *Server) publicMailRoute(w http.ResponseWriter, r *http.Request) bool {
	switch {
	case r.URL.Path == "/api/registration-code" && r.Method == "POST":
		var b struct {
			Email      string `json:"email"`
			Invitation string `json:"invitation"`
		}
		if decode(w, r, &b) && s.authBudget(w, r, b.Email) {
			s.sendChallenge(w, r, b.Email, "register", b.Invitation)
		}
	case r.URL.Path == "/api/password-resets" && r.Method == "POST":
		var b struct {
			Email string `json:"email"`
		}
		if decode(w, r, &b) && s.authBudget(w, r, b.Email) {
			s.sendChallenge(w, r, b.Email, "reset", "")
		}
	case r.URL.Path == "/api/password-resets/complete" && r.Method == "POST":
		var b struct {
			Token    string `json:"token"`
			Password string `json:"password"`
		}
		if !decode(w, r, &b) || !s.authBudget(w, r, "") {
			return true
		}
		id, code, ok := strings.Cut(b.Token, ".")
		if !ok {
			problem(w, 400, "reset_invalid", "重置链接无效或已过期，请重新申请")
			return true
		}
		user, err := s.Accounts.ResetWithProof(r.Context(), accounts.Proof{ChallengeID: id, Code: code}, b.Password)
		if errors.Is(err, accounts.ErrInput) {
			problem(w, 400, "password_invalid", "密码需要 12–128 个字符")
			return true
		}
		if err != nil {
			if errors.Is(err, accounts.ErrVerification) {
				problem(w, 400, "reset_invalid", "重置链接无效或已过期，请重新申请")
			} else {
				s.storageError(w)
			}
			return true
		}
		s.disconnectUser(user)
		s.cookie(w, "", -1)
		writeJSON(w, 200, map[string]bool{"ok": true})
	default:
		return false
	}
	return true
}

func (s *Server) sendChallenge(w http.ResponseWriter, r *http.Request, email, purpose, invitation string) {
	c, code, err := s.Accounts.BeginChallenge(r.Context(), email, purpose, invitation)
	if purpose == "reset" && (errors.Is(err, accounts.ErrCredentials) || errors.Is(err, accounts.ErrLimit) || errors.Is(err, accounts.ErrInput)) {
		writeJSON(w, 202, map[string]bool{"accepted": true})
		return
	}
	if err != nil {
		switch {
		case errors.Is(err, accounts.ErrLimit):
			w.Header().Set("Retry-After", "60")
			problem(w, 429, "mail_rate_limited", "请稍后重新获取邮件")
		case errors.Is(err, accounts.ErrInput):
			problem(w, 400, "email_invalid", "请填写有效的邮箱地址")
		case errors.Is(err, accounts.ErrCredentials):
			problem(w, 400, "verification_rejected", "邀请码或账号状态不可用")
		default:
			s.storageError(w)
		}
		return
	}
	message := MailMessage{To: c.Email, Subject: "WIA 邮箱验证码", Body: fmt.Sprintf("你的 World Is Agent 邮箱验证码为：%s\n\n验证码 10 分钟内有效。若非本人操作，请忽略这封邮件。", code)}
	if purpose == "reset" {
		message.Subject = "WIA 密码重置"
		message.Body = fmt.Sprintf("请打开以下链接设置新密码：\n%s/#reset?token=%s.%s\n\n链接 30 分钟内有效，提交新密码后原会话和 WIA Key 会失效。若非本人操作，请忽略这封邮件。", strings.TrimSuffix(s.Config.Origin, "/"), c.ID, code)
	}
	err = s.mailer.Send(r.Context(), message)
	if err != nil {
		_ = s.Accounts.FailChallenge(r.Context(), c.ID)
		if purpose == "reset" {
			log.Print("password reset email was not accepted")
			writeJSON(w, 202, map[string]bool{"accepted": true})
			return
		}
		if errors.Is(err, ErrMailNotConfigured) {
			problem(w, 503, "mail_not_configured", "邮件服务尚未配置，请联系管理员")
		} else {
			problem(w, 503, "mail_unavailable", "邮件发送暂不可用，请稍后重试")
		}
		return
	}
	if err = s.Accounts.ActivateChallenge(r.Context(), c.ID); err != nil {
		if purpose == "reset" {
			log.Print("password reset challenge could not be activated")
			writeJSON(w, 202, map[string]bool{"accepted": true})
			return
		}
		s.storageError(w)
		return
	}
	if purpose == "reset" {
		writeJSON(w, 202, map[string]bool{"accepted": true})
		return
	}
	writeJSON(w, 200, c)
}

func (s *Server) verificationRoute(w http.ResponseWriter, r *http.Request, session accounts.Session, secret string) bool {
	switch {
	case r.URL.Path == "/api/me/email-verification" && r.Method == "POST":
		if s.authBudget(w, r, session.User.Email) {
			s.sendChallenge(w, r, session.User.Email, "verify", "")
		}
	case r.URL.Path == "/api/me/email-verification/confirm" && r.Method == "POST":
		var proof accounts.Proof
		if !decode(w, r, &proof) || !s.authBudget(w, r, session.User.Email) {
			return true
		}
		err := s.Accounts.VerifyEmail(r.Context(), session.User.ID, proof)
		if err != nil {
			if errors.Is(err, accounts.ErrVerification) || errors.Is(err, accounts.ErrCredentials) {
				problem(w, 400, "verification_invalid", "验证码错误或已失效，请重新获取")
			} else {
				s.storageError(w)
			}
			return true
		}
		if err = s.Accounts.Logout(r.Context(), secret); err != nil {
			s.storageError(w)
			return true
		}
		u, err := s.Accounts.User(r.Context(), session.User.ID)
		if err != nil {
			s.storageError(w)
			return true
		}
		s.issueSession(w, r, u, 200)
	default:
		return false
	}
	return true
}
