// Package validator provides struct tag validation and custom rule registration for Helix Cluster OS.
package validator

import (
	"fmt"
	"net/mail"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

var (
	uuidRe   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	alphaNum = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
)

// RuleFunc is a custom validation function.
type RuleFunc func(value string) error

// Validator holds validation rules.
type Validator struct {
	rules map[string]RuleFunc
}

// New creates a new Validator.
func New() *Validator {
	v := &Validator{rules: make(map[string]RuleFunc)}
	v.registerDefaults()
	return v
}

// RegisterRule registers a custom validation rule by name.
func (v *Validator) RegisterRule(name string, fn RuleFunc) {
	v.rules[name] = fn
}

// IsValidID checks if id matches a simple alphanumeric pattern.
func (v *Validator) IsValidID(id string) bool {
	return alphaNum.MatchString(id)
}

// NotEmpty returns an error if s is empty.
func (v *Validator) NotEmpty(s string) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("value must not be empty")
	}
	return nil
}

// ValidateStruct validates a struct using `validate` tags.
// Supported tags: required, min, max, email, uuid, oneof.
func (v *Validator) ValidateStruct(s interface{}) error {
	if s == nil {
		return fmt.Errorf("nil struct")
	}
	val := reflect.ValueOf(s)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	if val.Kind() != reflect.Struct {
		return fmt.Errorf("expected struct, got %s", val.Kind())
	}
	typ := val.Type()
	var errs []string
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		fv := val.Field(i)
		if !fv.IsValid() || !fv.CanInterface() {
			continue
		}
		tag := field.Tag.Get("validate")
		if tag == "" {
			continue
		}
		if err := v.validateField(field.Name, fv, tag); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("validation failed: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (v *Validator) validateField(name string, val reflect.Value, tag string) error {
	// Dereference pointer.
	if val.Kind() == reflect.Ptr && !val.IsNil() {
		val = val.Elem()
	}
	var strVal string
	switch val.Kind() {
	case reflect.String:
		strVal = val.String()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		strVal = strconv.FormatInt(val.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		strVal = strconv.FormatUint(val.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		strVal = strconv.FormatFloat(val.Float(), 'f', -1, 64)
	default:
		strVal = fmt.Sprintf("%v", val.Interface())
	}

	rules := strings.Split(tag, ",")
	for _, rule := range rules {
		rule = strings.TrimSpace(rule)
		if rule == "" {
			continue
		}
		if err := v.checkRule(name, strVal, val, rule); err != nil {
			return err
		}
	}
	return nil
}

func (v *Validator) checkRule(name, strVal string, val reflect.Value, rule string) error {
	switch {
	case rule == "required":
		if strings.TrimSpace(strVal) == "" {
			return fmt.Errorf("field %s is required", name)
		}
	case strings.HasPrefix(rule, "min="):
		arg := rule[4:]
		if err := v.checkMin(name, strVal, val, arg); err != nil {
			return err
		}
	case strings.HasPrefix(rule, "max="):
		arg := rule[4:]
		if err := v.checkMax(name, strVal, val, arg); err != nil {
			return err
		}
	case rule == "email":
		if _, err := mail.ParseAddress(strVal); err != nil {
			return fmt.Errorf("field %s must be a valid email", name)
		}
	case rule == "uuid":
		if !uuidRe.MatchString(strVal) {
			return fmt.Errorf("field %s must be a valid UUID", name)
		}
	case strings.HasPrefix(rule, "oneof="):
		allowed := strings.Split(rule[6:], " ")
		found := false
		for _, a := range allowed {
			if strVal == a {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("field %s must be one of %v", name, allowed)
		}
	default:
		if fn, ok := v.rules[rule]; ok {
			if err := fn(strVal); err != nil {
				return fmt.Errorf("field %s invalid: %w", name, err)
			}
		}
	}
	return nil
}

func (v *Validator) checkMin(name, strVal string, val reflect.Value, arg string) error {
	switch val.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, _ := strconv.ParseInt(arg, 10, 64)
		if val.Int() < n {
			return fmt.Errorf("field %s must be >= %d", name, n)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, _ := strconv.ParseUint(arg, 10, 64)
		if val.Uint() < n {
			return fmt.Errorf("field %s must be >= %d", name, n)
		}
	case reflect.Float32, reflect.Float64:
		n, _ := strconv.ParseFloat(arg, 64)
		if val.Float() < n {
			return fmt.Errorf("field %s must be >= %f", name, n)
		}
	default:
		n, _ := strconv.Atoi(arg)
		if len(strVal) < n {
			return fmt.Errorf("field %s length must be >= %d", name, n)
		}
	}
	return nil
}

func (v *Validator) checkMax(name, strVal string, val reflect.Value, arg string) error {
	switch val.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, _ := strconv.ParseInt(arg, 10, 64)
		if val.Int() > n {
			return fmt.Errorf("field %s must be <= %d", name, n)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, _ := strconv.ParseUint(arg, 10, 64)
		if val.Uint() > n {
			return fmt.Errorf("field %s must be <= %d", name, n)
		}
	case reflect.Float32, reflect.Float64:
		n, _ := strconv.ParseFloat(arg, 64)
		if val.Float() > n {
			return fmt.Errorf("field %s must be <= %f", name, n)
		}
	default:
		n, _ := strconv.Atoi(arg)
		if len(strVal) > n {
			return fmt.Errorf("field %s length must be <= %d", name, n)
		}
	}
	return nil
}

func (v *Validator) registerDefaults() {
	// No default custom rules; user can register via RegisterRule.
}
