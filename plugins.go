package main

// plugins.go is the managed plugin manifest. `herrscher plugin add <module>`
// appends a blank import between the markers below; importing a plugin package
// triggers its init(), which self-registers it into contracts.Default. The host
// then discovers every compiled-in plugin at startup with no per-plugin wiring.
//
// Lines between "herrscher:plugins" and "herrscher:end" are managed by the CLI —
// edit by hand only if you know what you are doing.

import (
	// herrscher:plugins
	_ "github.com/Herrscherd/herrscher-claude-backend"
	_ "github.com/Herrscherd/herrscher-discord-gateway"
	_ "github.com/Herrscherd/herrscher-obsidian-memory"
	_ "github.com/Herrscherd/herrscher-orchestrator"
	_ "github.com/Herrscherd/herrscher/plugins/terminal"
	// herrscher:end
)
