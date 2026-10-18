package model

import (
	"context"
	"errors"
)

var (
	ErrResponseInvalidJSON = errors.New("model response is not valid JSON")
	ErrResponseTruncated   = errors.New("model response was truncated")
)

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

const (
	ControlUnspecified ControlKind = "unspecified"
	ControlContinue    ControlKind = "continue"
	ControlSettle      ControlKind = "settle"
)

const InternalSettleToolName = "__gameagent_settle"

type ControlKind string

type ControlDefinition struct {
	Kind        ControlKind
	Description string
}

type ControlDirective struct {
	Kind   ControlKind
	Reason string
}

// Provider 抽象一次模型生成调用。
//
// 不同厂商可以有不同 tool calling 协议，但进入 Agent Loop 前必须统一成 Response。
type Provider interface {
	Generate(ctx context.Context, req Request) (Response, error)
}

type Request struct {
	System   string
	Messages []Message
	Tools    []ToolDefinition
	Controls []ControlDefinition
}

type Message struct {
	Role             Role
	Content          string
	ToolCalls        []ToolCall
	ToolResults      []ToolResult
	ReasoningContent string `json:",omitempty"`
}

type Role string

type ToolDefinition struct {
	Name        string
	Description string
	InputSchema string
}

type Response struct {
	Decision         ModelDecision
	ReasoningContent string
}

type ModelDecision struct {
	ToolCalls []ToolCall
	Control   ControlDirective
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any
	// RawArguments is retained only when decoding fails; Arguments stays nil.
	RawArguments   string `json:",omitempty"`
	ArgumentsError string `json:",omitempty"`
}

type ToolResult struct {
	ToolCallID string
	Name       string
	Status     string
	Code       string
	Message    string
	Output     map[string]any
}
