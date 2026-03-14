package ipc

import "github.com/google/uuid"

func GenerateId() string {
	return uuid.New().String()
}
