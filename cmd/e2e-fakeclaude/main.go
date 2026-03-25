package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func main() {
	promptText := promptArg(os.Args[1:])
	targetURL := "https://en.wikipedia.org/wiki/Test"
	expectFailure := false
	if strings.Contains(strings.ToLower(promptText), "deny") {
		targetURL = "https://evil.com/"
		expectFailure = true
	}

	sock := os.Getenv("WARD_TOOL_SOCK")
	if sock == "" {
		fmt.Fprintln(os.Stderr, "WARD_TOOL_SOCK is required")
		os.Exit(1)
	}

	for range 10 {
		if fi, err := os.Stat(sock); err == nil && fi.Mode()&os.ModeSocket != 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	cmd := exec.Command("/usr/bin/Browser_Page_Read", "--url", targetURL)
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if expectFailure {
		if err == nil {
			fmt.Fprintf(os.Stderr, "expected Browser_Page_Read denial for %s\n", targetURL)
			os.Exit(1)
		}
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "Browser_Page_Read failed: %v\n%s\n", err, stderr.String())
		os.Exit(1)
	}

	fmt.Println(`{"type":"tool_use"}`)
	fmt.Println(`{"type":"message_stop","usage":{"input_tokens":12,"output_tokens":7}}`)
}

func promptArg(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "--print" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
