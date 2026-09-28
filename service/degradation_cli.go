package service

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// RunDegradationProbeCLI is the optional single-endpoint diagnostic command.
// Exit codes: 0 normal, 1 abnormal by the heuristic, 2 request/configuration error.
func RunDegradationProbeCLI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("degradation-probe", flag.ContinueOnError)
	// flag parsing errors may contain argument values. Do not echo URLs/secrets.
	flags.SetOutput(io.Discard)
	var target DegradationTarget
	var keyEnv string
	var help bool
	flags.StringVar(&target.Endpoint, "endpoint", "", "full target endpoint URL (HTTPS by default)")
	flags.StringVar(&target.Model, "model", "", "upstream model name")
	flags.StringVar(&target.Format, "format", "chat", "response protocol: chat, responses, anthropic")
	flags.StringVar(&keyEnv, "api-key-env", "DEGRADATION_API_KEY", "environment variable containing the API key (never put the key in arguments)")
	flags.DurationVar(&target.Timeout, "timeout", 30*time.Second, "request timeout (1s..300s)")
	flags.BoolVar(&target.AllowHTTP, "allow-http", false, "explicitly allow plaintext HTTP for operator-managed local endpoints")
	flags.BoolVar(&help, "help", false, "show usage")
	flags.BoolVar(&help, "h", false, "show usage")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "invalid degradation-probe arguments; use --help")
		return 2
	}
	if help {
		fmt.Fprintln(stdout, "Usage: newapi degradation-probe --endpoint URL --model MODEL [options]")
		flags.SetOutput(stdout)
		flags.PrintDefaults()
		fmt.Fprintln(stdout, "Heuristic only: any of can't / original number / starting number / assistant guidelines means normal; otherwise abnormal. Transport/protocol errors are inconclusive. Output never includes keys, URLs or response text. This command does not update the database.")
		return 0
	}
	target.APIKey = strings.TrimSpace(os.Getenv(keyEnv))
	result := ProbeDegradation(ctx, target)
	encoded, err := common.Marshal(result)
	if err != nil {
		fmt.Fprintln(stderr, "could not encode degradation result")
		return 2
	}
	if _, err := fmt.Fprintln(stdout, string(encoded)); err != nil {
		return 2
	}
	switch result.State {
	case "normal":
		return 0
	case "abnormal":
		return 1
	default:
		return 2
	}
}
