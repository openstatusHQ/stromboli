package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"strings"

	"connectrpc.com/connect"
)

// authInterceptor requires `Authorization: Bearer <token>` on every call.
type authInterceptor struct {
	token string
}

// NewAuthInterceptor returns an interceptor checking the API token. An empty
// token disables auth.
func NewAuthInterceptor(token string) connect.Interceptor {
	return &authInterceptor{token: token}
}

func (a *authInterceptor) check(header string) error {
	if a.token == "" {
		return nil
	}
	got := strings.TrimPrefix(header, "Bearer ")
	if subtle.ConstantTimeCompare([]byte(got), []byte(a.token)) != 1 {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("invalid or missing API token"))
	}
	return nil
}

func (a *authInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if !req.Spec().IsClient {
			if err := a.check(req.Header().Get("Authorization")); err != nil {
				return nil, err
			}
		}
		return next(ctx, req)
	}
}

func (a *authInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (a *authInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if err := a.check(conn.RequestHeader().Get("Authorization")); err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

// BearerToken is a client interceptor that sends the API token.
func BearerToken(token string) connect.Interceptor {
	return bearer(token)
}

type bearer string

func (b bearer) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if b != "" {
			req.Header().Set("Authorization", "Bearer "+string(b))
		}
		return next(ctx, req)
	}
}

func (b bearer) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		if b != "" {
			conn.RequestHeader().Set("Authorization", "Bearer "+string(b))
		}
		return conn
	}
}

func (b bearer) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
