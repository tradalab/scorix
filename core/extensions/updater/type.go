package updater

import (
	"errors"
)

type Config struct {
	AppcastURL      string `yaml:"appcast_url"`
	PublicKeyBase64 string `yaml:"public_key_base_64"`
	PlatformKey     string `yaml:"platform_key"`
	ForceElevate    bool   `yaml:"force_elevate"`
	CurrentVersion  string `yaml:"current_version"`
}

type StaticAppcast struct {
	Version   string                      `json:"version"`
	PubDate   string                      `json:"pub_date,omitempty"`
	Notes     string                      `json:"notes,omitempty"`
	Platforms map[string]PlatformArtifact `json:"platforms"`
}

type PlatformArtifact struct {
	URL              string `json:"url"`
	SignatureBase64  string `json:"signature,omitempty"`
	WithElevatedTask bool   `json:"with_elevated_task,omitempty"`
}

type DynamicAppcast struct {
	URL             string `json:"url"`
	Version         string `json:"version"`
	PubDate         string `json:"pub_date,omitempty"`
	Notes           string `json:"notes,omitempty"`
	SignatureBase64 string `json:"signature,omitempty"`
}

type Result struct {
	HasUpdate   bool   `json:"has_update"`
	NewVersion  string `json:"new_version"`
	Notes       string `json:"notes"`
	ArtifactURL string `json:"artifact_url"`
	SigBase64   string `json:"sig_base64"`
	Elevate     bool   `json:"elevate"`
	LocalPath   string `json:"local_path"`
}

var (
	ErrNoUpdate           = errors.New("no update available")
	ErrSignatureMissing   = errors.New("signature missing in appcast")
	ErrSignatureInvalid   = errors.New("signature invalid")
	ErrUnknownAppcastType = errors.New("unknown appcast shape")
)
