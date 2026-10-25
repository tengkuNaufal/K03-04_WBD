package authz

import (
	"context"
	"errors"
	"testing"
)

type stubSource struct {
	levels map[int64][]Level // by user id
	err    error
}

func (s stubSource) GroupLevels(_ context.Context, userID, _ int64) ([]Level, error) {
	return s.levels[userID], s.err
}

func TestEffectiveLevelTakesMostPermissive(t *testing.T) {
	cases := []struct {
		in   []Level
		want Level
	}{
		{nil, LevelNone},
		{[]Level{LevelView}, LevelView},
		{[]Level{LevelView, LevelEdit, LevelView}, LevelEdit},
	}
	for _, c := range cases {
		if got := EffectiveLevel(c.in); got != c.want {
			t.Errorf("EffectiveLevel(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseLevel(t *testing.T) {
	for _, l := range []Level{LevelView, LevelEdit} {
		if got, err := ParseLevel(l.String()); err != nil || got != l {
			t.Fatalf("roundtrip %v: got %v, %v", l, got, err)
		}
	}
	if _, err := ParseLevel("owner"); err == nil {
		t.Fatal("unknown level accepted")
	}
}

func TestAuthorize(t *testing.T) {
	p := NewPolicy(stubSource{levels: map[int64][]Level{
		1: {LevelView},
		2: {LevelEdit},
		3: {LevelView, LevelEdit}, // member of two groups
	}})
	viewer, editor, both := &Principal{UserID: 1}, &Principal{UserID: 2}, &Principal{UserID: 3}
	outsider, admin := &Principal{UserID: 4}, &Principal{UserID: 5, IsAdmin: true}

	cases := []struct {
		name string
		p    *Principal
		a    Action
		ok   bool
	}{
		{"viewer views", viewer, ActionView, true},
		{"viewer cannot edit", viewer, ActionEdit, false},
		{"editor edits", editor, ActionEdit, true},
		{"two groups -> edit wins", both, ActionEdit, true},
		{"no group -> denied", outsider, ActionView, false},
		{"admin is not a db level", admin, ActionView, false},
		{"viewer cannot manage platform", viewer, ActionManagePlatform, false},
		{"admin manages platform", admin, ActionManagePlatform, true},
		{"unknown action denied", admin, Action("db.drop"), false},
	}
	for _, c := range cases {
		err := p.Authorize(context.Background(), c.p, c.a, 10)
		if c.ok != (err == nil) || (!c.ok && !errors.Is(err, ErrForbidden)) {
			t.Errorf("%s: got %v", c.name, err)
		}
	}
}

func TestAuthorizeErrors(t *testing.T) {
	if err := NewPolicy(stubSource{}).Authorize(context.Background(), nil, ActionView, 1); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("want ErrUnauthenticated, got %v", err)
	}
	boom := errors.New("db down")
	err := NewPolicy(stubSource{err: boom}).Authorize(context.Background(), &Principal{UserID: 1}, ActionView, 1)
	if !errors.Is(err, boom) {
		t.Fatalf("want source error, got %v", err)
	}
	if Allows(LevelEdit, ActionManagePlatform) {
		t.Fatal("database levels never allow platform actions")
	}
}
