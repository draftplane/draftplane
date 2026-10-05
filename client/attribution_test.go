package client_test

import (
	"context"
	"errors"
	"testing"

	"github.com/draftplane/draftplane/client"
	"github.com/draftplane/draftplane/domain"
)

// collidingKey stands in for a key defined outside client: a value stashed
// under it must never satisfy AttributionFrom.
type collidingKey struct{}

func TestAttribution(t *testing.T) {
	want := domain.Attribution{ActorID: "u1", ActorLogin: "ada-codes", ActorDisplay: "Ada", Agent: "blue-parakeet-f9"}

	tests := []struct {
		name    string
		ctx     context.Context
		wantErr bool
	}{
		{
			name: "round trips through WithAttribution",
			ctx:  client.WithAttribution(context.Background(), want),
		},
		{
			name:    "bare context fails closed",
			ctx:     context.Background(),
			wantErr: true,
		},
		{
			name:    "value under a colliding key type is not satisfied",
			ctx:     context.WithValue(context.Background(), collidingKey{}, want),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := client.AttributionFrom(tt.ctx)
			if tt.wantErr {
				if !errors.Is(err, client.ErrNoAttribution) {
					t.Fatalf("AttributionFrom() err = %v, want ErrNoAttribution", err)
				}
				if got != (domain.Attribution{}) {
					t.Fatalf("AttributionFrom() = %+v, want zero value", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("AttributionFrom() unexpected err = %v", err)
			}
			if got != want {
				t.Fatalf("AttributionFrom() = %+v, want %+v", got, want)
			}
		})
	}
}
