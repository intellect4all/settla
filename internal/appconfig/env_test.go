package appconfig

import (
	"testing"
)

func TestParseEnv(t *testing.T) {
	tests := []struct {
		input   string
		want    Env
		wantErr bool
	}{
		{"development", EnvDevelopment, false},
		{"staging", EnvStaging, false},
		{"production", EnvProduction, false},
		{"", "", true},
		{"prod", "", true},
		{"PRODUCTION", "", true},
		{"test", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseEnv(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseEnv(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ParseEnv(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestEnvMethods(t *testing.T) {
	tests := []struct {
		env              Env
		isProd           bool
		isStaging        bool
		isDev            bool
		requiresAuth     bool
		requiresSSL      bool
		requiresRLS      bool
		allowsReflection bool
	}{
		{EnvDevelopment, false, false, true, false, false, false, true},
		{EnvStaging, false, true, false, true, false, true, false},
		{EnvProduction, true, false, false, true, true, true, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.env), func(t *testing.T) {
			if tt.env.IsProd() != tt.isProd {
				t.Errorf("%s.IsProd() = %v, want %v", tt.env, tt.env.IsProd(), tt.isProd)
			}
			if tt.env.IsStaging() != tt.isStaging {
				t.Errorf("%s.IsStaging() = %v, want %v", tt.env, tt.env.IsStaging(), tt.isStaging)
			}
			if tt.env.IsDev() != tt.isDev {
				t.Errorf("%s.IsDev() = %v, want %v", tt.env, tt.env.IsDev(), tt.isDev)
			}
			if tt.env.RequiresAuth() != tt.requiresAuth {
				t.Errorf("%s.RequiresAuth() = %v, want %v", tt.env, tt.env.RequiresAuth(), tt.requiresAuth)
			}
			if tt.env.RequiresSSL() != tt.requiresSSL {
				t.Errorf("%s.RequiresSSL() = %v, want %v", tt.env, tt.env.RequiresSSL(), tt.requiresSSL)
			}
			if tt.env.RequiresRLS() != tt.requiresRLS {
				t.Errorf("%s.RequiresRLS() = %v, want %v", tt.env, tt.env.RequiresRLS(), tt.requiresRLS)
			}
			if tt.env.AllowsReflection() != tt.allowsReflection {
				t.Errorf("%s.AllowsReflection() = %v, want %v", tt.env, tt.env.AllowsReflection(), tt.allowsReflection)
			}
		})
	}
}
