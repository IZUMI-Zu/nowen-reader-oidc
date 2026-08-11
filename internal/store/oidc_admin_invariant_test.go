package store

import (
	"context"
	"errors"
	"sync"
	"testing"

	oidcauth "github.com/nowen-reader/nowen-reader/internal/auth/oidc"
	"github.com/nowen-reader/nowen-reader/internal/model"
)

func TestUserMutationsPreserveLastBoundOIDCAdministrator(t *testing.T) {
	setupOIDCStoreTest(t)
	const issuer = "https://identity.example.com"
	for _, user := range []*model.User{
		{ID: "operator", Username: "operator", Password: "hash", Nickname: "Operator", Role: "admin"},
		{ID: "bound-admin", Username: "bound-admin", Password: "hash", Nickname: "Bound", Role: "admin"},
	} {
		if err := CreateUser(user); err != nil {
			t.Fatal(err)
		}
	}
	if err := LinkOIDCIdentity(context.Background(), "bound-admin", oidcauth.VerifiedIdentity{
		Issuer: issuer, Subject: "bound-subject",
	}); err != nil {
		t.Fatal(err)
	}
	if err := UpdateUserRolePreservingOIDCAdmin("bound-admin", "user", issuer); !errors.Is(err, ErrOIDCLastBoundAdministrator) {
		t.Fatalf("demote error = %v, want ErrOIDCLastBoundAdministrator", err)
	}
	if user, err := GetUserByID("bound-admin"); err != nil || user == nil || user.Role != "admin" {
		t.Fatalf("rejected demotion changed user: %+v, %v", user, err)
	}
	if err := DeleteUserPreservingOIDCAdmin("bound-admin", issuer); !errors.Is(err, ErrOIDCLastBoundAdministrator) {
		t.Fatalf("delete error = %v, want ErrOIDCLastBoundAdministrator", err)
	}
	if user, err := GetUserByID("bound-admin"); err != nil || user == nil {
		t.Fatalf("rejected deletion removed user: %+v, %v", user, err)
	}
}

func TestConcurrentDemotionsLeaveOneBoundOIDCAdministrator(t *testing.T) {
	setupOIDCStoreTest(t)
	const issuer = "https://identity.example.com"
	for _, id := range []string{"admin-a", "admin-b"} {
		if err := CreateUser(&model.User{ID: id, Username: id, Password: "hash", Nickname: id, Role: "admin"}); err != nil {
			t.Fatal(err)
		}
		if err := LinkOIDCIdentity(context.Background(), id, oidcauth.VerifiedIdentity{Issuer: issuer, Subject: id + "-subject"}); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	errorsByUser := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, id := range []string{"admin-a", "admin-b"} {
		go func(userID string) {
			ready.Done()
			<-start
			errorsByUser <- UpdateUserRolePreservingOIDCAdmin(userID, "user", issuer)
		}(id)
	}
	ready.Wait()
	close(start)
	var succeeded, rejected int
	for range 2 {
		err := <-errorsByUser
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrOIDCLastBoundAdministrator):
			rejected++
		default:
			t.Fatalf("concurrent demotion error = %v", err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("concurrent demotions: succeeded=%d rejected=%d", succeeded, rejected)
	}
	var remaining int
	if err := DB().QueryRow(`SELECT COUNT(*) FROM "User" user
		JOIN "ExternalIdentity" identity ON identity."userId" = user."id"
		WHERE user."role" = 'admin' AND identity."issuer" = ?`, issuer).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("remaining bound administrators = %d, %v", remaining, err)
	}
}
