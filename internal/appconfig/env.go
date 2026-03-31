package appconfig

import "fmt"

// Env represents the deployment environment. Used for config validation
// and conditional behavior (e.g., requiring TLS, NATS auth, RLS enforcement).
type Env string

const (
	EnvDevelopment Env = "development"
	EnvStaging     Env = "staging"
	EnvProduction  Env = "production"
)

// ParseEnv parses a string into an Env. Returns an error for unknown values.
func ParseEnv(s string) (Env, error) {
	switch Env(s) {
	case EnvDevelopment, EnvStaging, EnvProduction:
		return Env(s), nil
	default:
		return "", fmt.Errorf("unknown environment %q: must be one of development, staging, production", s)
	}
}

// IsProd returns true for production.
func (e Env) IsProd() bool { return e == EnvProduction }

// IsStaging returns true for staging.
func (e Env) IsStaging() bool { return e == EnvStaging }

// IsDev returns true for development.
func (e Env) IsDev() bool { return e == EnvDevelopment }

// RequiresAuth returns true for environments that require NATS authentication,
// mandatory JWT secrets, and other security-sensitive configuration.
func (e Env) RequiresAuth() bool { return e.IsProd() || e.IsStaging() }

// RequiresSSL returns true for environments where database connections must
// use sslmode=verify-ca or sslmode=verify-full (never sslmode=disable).
func (e Env) RequiresSSL() bool { return e.IsProd() }

// RequiresRLS returns true for environments where Row-Level Security must
// be enforced via a dedicated settla_app database role.
func (e Env) RequiresRLS() bool { return e.IsProd() || e.IsStaging() }

// AllowsReflection returns true for environments where gRPC reflection
// is enabled for debugging tools like grpcurl.
func (e Env) AllowsReflection() bool { return e.IsDev() }

// String returns the raw environment string.
func (e Env) String() string { return string(e) }
