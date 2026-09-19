package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/ChrisVilches/agi/transport"
	"github.com/ChrisVilches/agi/transport/litellm"
	"github.com/ChrisVilches/agi/transport/openai"
)

// EnvBackend names which backend a run talks to. It is an environment variable
// and not a flag because it is a property of the machine agi is installed on
// rather than of one question: whoever set up a gateway or a provider key set
// it up once, and every run after that wants the same one. A flag would have
// to be repeated on every invocation to say something that never changes.
const EnvBackend = "AGI_BACKEND"

// backends is every backend a run can be pointed at, by the name EnvBackend is
// set to. Each entry reads its own environment variables and nothing else's,
// so configuring one says nothing about the others: a gateway's address is the
// gateway's business, and a provider key is the provider's.
//
// This is the one list of them. Adding a backend means adding a line here and
// a row to the environment table in AGENTS.md, and nothing in the agent loop
// changes, because the loop only ever sees a transport.Backend.
var backends = map[string]func() (transport.Backend, error){
	litellm.Name: func() (transport.Backend, error) { return litellm.FromEnv() },
	openai.Name:  func() (transport.Backend, error) { return openai.FromEnv() },
}

// openBackend settles which backend the run talks to and configures it.
//
// EnvBackend has no default. Two backends exist and they are configured from
// different variables, so choosing one silently would make the more likely
// mistake — setting up one backend and forgetting to ask for it — report the
// other one's variable as missing, which is advice that leads away from the
// problem. Being told to name the backend is one sentence; being sent after
// the wrong variable is an afternoon.
func openBackend() (transport.Backend, error) {
	name := strings.TrimSpace(os.Getenv(EnvBackend))
	if name == "" {
		return nil, fmt.Errorf("no backend chosen: set %s to one of %s", EnvBackend, backendNames())
	}

	open, ok := backends[name]
	if !ok {
		return nil, fmt.Errorf("unknown backend %q: set %s to one of %s", name, EnvBackend, backendNames())
	}
	return open()
}

// backendNames lists what EnvBackend accepts, in a fixed order so the advice
// reads the same way twice. A map has none of its own.
func backendNames() string {
	names := make([]string, 0, len(backends))
	for name := range backends {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
