package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/liam-auto/liam/internal/config"
	"github.com/liam-auto/liam/internal/db"
	"github.com/liam-auto/liam/internal/harvest"
	"github.com/liam-auto/liam/internal/proxy"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: liam <serve|setup>")
		os.Exit(1)
	}
	switch os.Args[1] {
	case "serve":
		serve()
	case "setup":
		setup()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func serve() {
	cfg := config.Load()
	database, err := db.New(cfg)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer database.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("LIAM listening on :%d", cfg.Port)
	if err := proxy.ListenAndServe(ctx, cfg, database); err != nil {
		log.Fatalf("server: %v", err)
	}
	log.Println("LIAM stopped gracefully")
}

func setup() {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("\n=== LIAM Setup ===\n")

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot find home dir: %v\n", err)
		os.Exit(1)
	}
	liamDir := filepath.Join(home, ".liam")
	harvestDir := filepath.Join(liamDir, "harvest")

	// Ensure ~/.liam exists
	if err := os.MkdirAll(liamDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create %s: %v\n", liamDir, err)
		os.Exit(1)
	}

	// --- Harvest Python module ---
	fmt.Println("[1] Extracting harvest module...")
	if err := os.MkdirAll(harvestDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create harvest dir: %v\n", err)
		os.Exit(1)
	}
	if err := harvest.ExtractHarvestModule(harvestDir); err != nil {
		fmt.Fprintf(os.Stderr, "extract harvest: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("    ✓ harvest module extracted\n")

	// --- Python dependencies + Camoufox ---
	if err := harvest.Setup(harvestDir); err != nil {
		fmt.Fprintf(os.Stderr, "\nsetup failed: %v\n", err)
		os.Exit(1)
	}

	// --- Dashboard password ---
	fmt.Print("[*] Dashboard password (leave blank to keep default '123456'): ")
	password, _ := reader.ReadString('\n')
	password = strings.TrimSpace(password)

	if password != "" {
		cfg := config.Load()
		database, err := db.New(cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "database: %v\n", err)
			os.Exit(1)
		}
		if err := database.SetSetting("dashboard_password", password); err != nil {
			fmt.Fprintf(os.Stderr, "set password: %v\n", err)
			database.Close()
			os.Exit(1)
		}
		database.Close()
		fmt.Println("    ✓ dashboard password set\n")
	} else {
		fmt.Println("    (keeping default: 123456)\n")
	}

	// --- Supabase sync (optional) ---
	fmt.Print("[*] Supabase URL (leave blank to skip): ")
	supabaseURL, _ := reader.ReadString('\n')
	supabaseURL = strings.TrimSpace(supabaseURL)

	if supabaseURL != "" {
		fmt.Print("[*] Supabase anon key: ")
		supabaseKey, _ := reader.ReadString('\n')
		supabaseKey = strings.TrimSpace(supabaseKey)

		envPath := filepath.Join(liamDir, ".env")
		if err := appendEnvVars(envPath, map[string]string{
			"SUPABASE_URL": supabaseURL,
			"SUPABASE_KEY": supabaseKey,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "write .env: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("    ✓ Supabase config saved to ~/.liam/.env\n")
	}

	fmt.Println("=== Setup complete ===")
	fmt.Println("\nRun 'liam serve' to start the proxy.")
	fmt.Println("Dashboard: http://localhost:666/dashboard")
}

// appendEnvVars writes or updates KEY=VALUE pairs in the given .env file.
// Existing keys are overwritten; new keys are appended.
func appendEnvVars(path string, vars map[string]string) error {
	// Read existing content
	existing := map[string]string{}
	var lines []string
	if data, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				lines = append(lines, line)
				continue
			}
			parts := strings.SplitN(trimmed, "=", 2)
			if len(parts) == 2 {
				existing[parts[0]] = parts[1]
				lines = append(lines, line)
			} else {
				lines = append(lines, line)
			}
		}
	}

	// Overwrite or append each var
	for k, v := range vars {
		if _, found := existing[k]; found {
			for i, line := range lines {
				if strings.HasPrefix(strings.TrimSpace(line), k+"=") {
					lines[i] = k + "=" + v
				}
			}
		} else {
			lines = append(lines, k+"="+v)
		}
	}

	content := strings.Join(lines, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return os.WriteFile(path, []byte(content), 0600)
}
