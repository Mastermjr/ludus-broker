package discord

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

func CheckClock(ctx context.Context, maximumSkew time.Duration) (time.Duration, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://discord.com/api/v10/gateway", nil)
	if err != nil {
		return 0, err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("query Discord time: %w", err)
	}
	response.Body.Close()
	remote, err := http.ParseTime(response.Header.Get("Date"))
	if err != nil {
		return 0, fmt.Errorf("parse Discord Date header: %w", err)
	}
	return validateClockSkew(time.Now().UTC(), remote.UTC(), maximumSkew)
}

func validateClockSkew(local, remote time.Time, maximum time.Duration) (time.Duration, error) {
	skew := local.Sub(remote)
	absolute := skew
	if absolute < 0 {
		absolute = -absolute
	}
	if absolute > maximum {
		return skew, fmt.Errorf("local clock differs from Discord by %s (maximum %s)", skew.Round(time.Second), maximum)
	}
	return skew, nil
}
