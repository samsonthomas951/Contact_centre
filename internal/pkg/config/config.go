// Package config loads service configuration from environment variables.
//
// Convention: every service calls Load[T](prefix) with its own typed struct.
// Values come from environment first (12-factor) and are validated via
// struct tags. Secret values must be mounted as files and read via the
// SecretFile helper — never embed them in environment variables in
// production.
package config

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Load populates dst from environment variables, honoring `env:"NAME"` and
// `default:"value"` struct tags. Fields without an `env` tag are skipped.
// Required fields must be tagged `required:"true"`; missing values return an
// error naming every field that was missing.
func Load(prefix string, dst any) error {
	v := reflect.ValueOf(dst)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("config: Load expects a pointer to struct, got %T", dst)
	}
	v = v.Elem()
	t := v.Type()

	var missing []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		envName := f.Tag.Get("env")
		if envName == "" {
			continue
		}
		if prefix != "" {
			envName = strings.ToUpper(prefix) + "_" + envName
		}

		raw, ok := os.LookupEnv(envName)
		if !ok {
			raw = f.Tag.Get("default")
		}
		if raw == "" && f.Tag.Get("required") == "true" {
			missing = append(missing, envName)
			continue
		}
		if raw == "" {
			continue
		}

		if err := assign(v.Field(i), raw); err != nil {
			return fmt.Errorf("config: %s: %w", envName, err)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("config: missing required env vars: %s", strings.Join(missing, ", "))
	}
	return nil
}

// SecretFile reads a secret from the path in `env`, or directly from `env`
// if the file does not exist. This matches the Docker-secrets convention of
// pointing `*_FILE` env vars at /run/secrets/... while still allowing tests
// to inject values directly.
func SecretFile(env string) (string, error) {
	v := os.Getenv(env)
	if v == "" {
		return "", fmt.Errorf("config: env %s is empty", env)
	}
	if b, err := os.ReadFile(v); err == nil {
		return strings.TrimRight(string(b), "\n"), nil
	}
	return v, nil
}

func assign(fv reflect.Value, raw string) error {
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return err
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if fv.Type() == reflect.TypeFor[time.Duration]() {
			d, err := time.ParseDuration(raw)
			if err != nil {
				return err
			}
			fv.SetInt(int64(d))
			return nil
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return err
		}
		fv.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return err
		}
		fv.SetUint(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return err
		}
		fv.SetFloat(f)
	default:
		return fmt.Errorf("unsupported field kind %s", fv.Kind())
	}
	return nil
}
