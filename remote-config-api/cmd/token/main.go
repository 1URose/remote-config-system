package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/auth"
	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/config"
)

func main() {
	subject := flag.String("subject", "admin@example.com", "token subject")
	roles := flag.String("roles", auth.RoleAdmin, "comma-separated roles")
	ttl := flag.Duration("ttl", time.Hour, "token lifetime")
	flag.Parse()

	cfg := config.Load()
	manager := auth.NewManager(cfg.JWTSecret)
	token, err := manager.Generate(*subject, splitRoles(*roles), *ttl)
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate token: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(token)
}

func splitRoles(value string) []string {
	parts := strings.Split(value, ",")
	roles := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			roles = append(roles, part)
		}
	}
	return roles
}
