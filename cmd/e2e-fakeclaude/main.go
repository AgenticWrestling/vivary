package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func main() {
	promptText := promptArg(os.Args[1:])
	targetURL := "https://en.wikipedia.org/wiki/Test"
	expectFailure := false
	if strings.Contains(strings.ToLower(promptText), "deny domain") {
		targetURL = "https://evil.com/"
		expectFailure = true
	} else if strings.Contains(strings.ToLower(promptText), "deny path") {
		targetURL = "https://en.wikipedia.org/other"
		expectFailure = true
	}

	if strings.Contains(strings.ToLower(promptText), "browser") {
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
	} else if strings.Contains(strings.ToLower(promptText), "write") {
		path := "test.txt"
		if strings.Contains(strings.ToLower(promptText), "deny") {
			path = "/etc/passwd"
			expectFailure = true
		}
		cmd := exec.Command("/usr/bin/Filesystem_File_Write", "--path", path, "--content", "hello")
		cmd.Env = os.Environ()
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		if expectFailure {
			if err == nil {
				fmt.Fprintf(os.Stderr, "expected Filesystem_File_Write denial for %s\n", path)
				os.Exit(1)
			}
		} else if err != nil {
			fmt.Fprintf(os.Stderr, "Filesystem_File_Write failed: %v\n%s\n", err, stderr.String())
			os.Exit(1)
		}
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
