package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	h := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return fmt.Sprintf("argon2id$v=19$m=65536,t=3,p=2$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(h)), nil
}

func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "argon2id" {
		return false
	}
	salt, e1 := base64.RawStdEncoding.DecodeString(parts[3])
	want, e2 := base64.RawStdEncoding.DecodeString(parts[4])
	if e1 != nil || e2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func hashPasswordCommand(args []string) error {
	fs := flag.NewFlagSet("hash-password", flag.ContinueOnError)
	password := fs.String("password", "", "password to hash")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(*password) < 16 {
		return errors.New("password must be at least 16 characters")
	}
	h, err := hashPassword(*password)
	if err == nil {
		fmt.Println(h)
	}
	return err
}
