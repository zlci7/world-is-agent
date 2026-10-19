package hosted

import (
	"errors"
	"gameagent/runtime/internal/accounts"
	"net/http"
	"time"
)

func (s *Server) adminLogin(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decode(w, r, &b) || !s.authBudget(w, r, b.Email) {
		return
	}
	user, err := s.Accounts.Login(r.Context(), b.Email, b.Password)
	if err != nil && !errors.Is(err, accounts.ErrCredentials) && !errors.Is(err, accounts.ErrInput) {
		s.storageError(w)
		return
	}
	if err != nil || !user.IsAdmin || user.VerifiedAt == 0 {
		problem(w, 401, "admin_login_rejected", "账号、密码或管理权限不正确")
		return
	}
	hours := min(s.Config.SessionHours, 8)
	secret, err := s.Accounts.NewAdminSession(r.Context(), user.ID, time.Duration(hours)*time.Hour)
	if err != nil {
		s.storageError(w)
		return
	}
	s.setAdminCookie(w, secret, hours*3600)
	session, err := s.Accounts.Session(r.Context(), secret)
	if err != nil {
		s.storageError(w)
		return
	}
	writeJSON(w, 200, session)
}
func (s *Server) setAdminCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: adminCookie, Value: value, Path: "/api/admin", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: maxAge})
}
