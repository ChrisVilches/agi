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

// EnvDefaultModel names the model every run uses. It is an environment
// variable for the same reason EnvBackend is: whoever set up a gateway or a
// provider key chose a model at the same time, and every run after that wants
// the same one.
//
// It is *default* rather than *model* because a flag will override it later,
// and the variable is the floor under that flag rather than the only way to
// answer. Naming it for what it is now would make the day the flag arrives a
// renaming.
//
// There is no default for the default. agi runs on the model it was told to
// run on and never on one it chose: the alternative is a value that was right
// when it was written into this repository and silently wrong afterwards —
// which is precisely what `deepseek-chat` became, a name that still answers
// but no longer names the model that answers it. A run that spends money and
// a context window on a model nobody picked is worse than a run that stops and
// says it has not been told which.
const EnvDefaultModel = "AGI_DEFAULT_MODEL"

// backends is every backend a run can be pointed at, by the name EnvBackend is
// set to. Each entry reads its own environment variables and nothing else's,
// so configuring one says nothing about the others: a gateway's address is the
// gateway's business, and a provider key is the provider's.
//
// This is the one list of them. Adding a backend means adding a line here and
// a row to the environment table in AGENTS.md, and nothing in the agent loop
// changes, because the loop only ever sees a transport.Backend.
// Each is handed the model rather than reading one, so that "which model" has
// a single answer however many backends there come to be.
var backends = map[string]func(model string) (transport.Backend, error){
	litellm.Name: func(model string) (transport.Backend, error) { return litellm.FromEnv(model) },
	openai.Name:  func(model string) (transport.Backend, error) { return openai.FromEnv(model) },
}

// openBackend settles which backend the run talks to, on which model, and
// configures it. It is what every run uses, and a run has to have both.
func openBackend() (transport.Backend, error) {
	name, err := chosenBackend()
	if err != nil {
		return nil, err
	}
	model := strings.TrimSpace(os.Getenv(EnvDefaultModel))

	// The endpoint is configured first, though the model is already in hand,
	// because that is the order the two can be fixed in. The advice a missing
	// model earns is to run `agi models`, and a machine whose endpoint address
	// is not set cannot follow it — it would be sent to a command failing for
	// the other reason. A backend reports a missing address whatever model it
	// was handed, so asking for one here costs nothing.
	backend, err := backends[name](model)
	if err != nil {
		return nil, err
	}
	if model == "" {
		return nil, fmt.Errorf("no model chosen: set %s to a model %s serves, as `agi models` lists it", EnvDefaultModel, name)
	}
	return backend, nil
}

// openEndpoint configures the backend without choosing a model, for the one
// question that is not a run.
//
// `agi models` must work on a machine where only the backend is configured, or
// the advice every missing-model error gives would send the user to a command
// that refuses them for the same reason — a new user told to run `agi models`
// and answered with "set AGI_DEFAULT_MODEL, as `agi models` lists it" has been
// sent in a circle. Listing a catalogue needs the address and the key and
// genuinely does not need a model, so it does not ask for one.
func openEndpoint() (transport.Backend, error) {
	name, err := chosenBackend()
	if err != nil {
		return nil, err
	}
	return backends[name]("")
}

// chosenBackend reads which backend was asked for and checks that it is one
// agi has.
//
// EnvBackend has no default. Two backends exist and they are configured from
// different variables, so choosing one silently would make the more likely
// mistake — setting up one backend and forgetting to ask for it — report the
// other one's variable as missing, which is advice that leads away from the
// problem. Being told to name the backend is one sentence; being sent after
// the wrong variable is an afternoon.
func chosenBackend() (string, error) {
	name := strings.TrimSpace(os.Getenv(EnvBackend))
	if name == "" {
		return "", fmt.Errorf("no backend chosen: set %s to one of %s", EnvBackend, backendNames())
	}
	if _, ok := backends[name]; !ok {
		return "", fmt.Errorf("unknown backend %q: set %s to one of %s", name, EnvBackend, backendNames())
	}
	return name, nil
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
