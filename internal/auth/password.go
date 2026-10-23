package auth

import (
	"errors"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

var Cost = 12

const (
	MinPasswordLength = 8
	maxPasswordBytes  = 72 // bcrypt ignores bytes after 72
)

var (
	ErrPasswordTooShort = errors.New("password minimal 8 karakter")
	ErrPasswordTooLong  = errors.New("password maksimal 72 byte")
)

func ValidatePassword(pw string) error {
	if utf8.RuneCountInString(pw) < MinPasswordLength {
		return ErrPasswordTooShort
	}
	if len(pw) > maxPasswordBytes {
		return ErrPasswordTooLong
	}
	return nil
}

func HashPassword(pw string) (string, error) {
	if err := ValidatePassword(pw); err != nil {
		return "", err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), Cost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

func VerifyPassword(hash, pw string) bool {
	if len(pw) > maxPasswordBytes {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy-password-for-timing"), bcrypt.DefaultCost)

func dummyVerify(pw string) {
	_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(pw))
}
