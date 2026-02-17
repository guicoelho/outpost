package proxy

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"golang.org/x/time/rate"
)

// PolicyError allows callers to translate policy failures into proper status codes.
type PolicyError struct {
	StatusCode int
	Message    string
}

func (e *PolicyError) Error() string {
	return e.Message
}

func newForbiddenf(format string, args ...any) error {
	return &PolicyError{StatusCode: 403, Message: fmt.Sprintf(format, args...)}
}

func newRateLimitedf(format string, args ...any) error {
	return &PolicyError{StatusCode: 429, Message: fmt.Sprintf(format, args...)}
}

// CheckMethod validates that method is in the allowed list.
func CheckMethod(method string, allowed []string) error {
	if len(allowed) == 0 {
		return nil
	}

	needle := strings.ToUpper(method)
	for _, m := range allowed {
		if needle == strings.ToUpper(strings.TrimSpace(m)) {
			return nil
		}
	}

	return newForbiddenf("method %s is not allowed", method)
}

// CheckPath validates path against a doublestar pattern list.
func CheckPath(path string, patterns []string) error {
	if len(patterns) == 0 {
		return nil
	}

	for _, pattern := range patterns {
		ok, err := doublestar.Match(pattern, path)
		if err != nil {
			return newForbiddenf("invalid path policy pattern %q", pattern)
		}
		if ok {
			return nil
		}
	}

	return newForbiddenf("path %s is not allowed", path)
}

// RateLimiter keeps one token bucket per managed tool.
type RateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
}

func NewRateLimiter() *RateLimiter {
	return &RateLimiter{limiters: make(map[string]*rate.Limiter)}
}

// Check enforces a policy string like "200/hour" for a given tool.
func (r *RateLimiter) Check(toolName, rawPolicy string) error {
	if strings.TrimSpace(rawPolicy) == "" {
		return nil
	}

	limit, window, err := parseRateLimit(rawPolicy)
	if err != nil {
		return newForbiddenf("invalid rate_limit %q: %v", rawPolicy, err)
	}

	r.mu.Lock()
	limiter, ok := r.limiters[toolName]
	if !ok {
		limiter = rate.NewLimiter(rate.Every(window/time.Duration(limit)), limit)
		r.limiters[toolName] = limiter
	}
	r.mu.Unlock()

	if !limiter.Allow() {
		return newRateLimitedf("rate limit exceeded for %s", toolName)
	}

	return nil
}

func parseRateLimit(input string) (int, time.Duration, error) {
	parts := strings.Split(strings.TrimSpace(input), "/")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected format <count>/<window>")
	}

	count, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || count <= 0 {
		return 0, 0, fmt.Errorf("invalid count")
	}

	switch strings.ToLower(strings.TrimSpace(parts[1])) {
	case "s", "sec", "second", "seconds":
		return count, time.Second, nil
	case "m", "min", "minute", "minutes":
		return count, time.Minute, nil
	case "h", "hr", "hour", "hours":
		return count, time.Hour, nil
	default:
		return 0, 0, fmt.Errorf("unsupported window")
	}
}
