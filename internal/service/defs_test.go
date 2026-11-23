package service

// nasty is a Definition full of characters that each format must quote.
func nasty() Definition {
	return Definition{
		Exe:  `/Users/Ed "Q" Smith/bin/near&host<1>`,
		Args: []string{"--web-port", "0"},
		Env: []EnvVar{
			{"PATH", "/usr/bin:50%:$HOME:é"},
			{"XDG_CONFIG_HOME", "/Users/e/Library/Application Support"},
		},
		LogPath: "/Users/e/Library/Logs/50%/core.log",
	}
}

func plain() Definition {
	return Definition{Exe: "/usr/local/bin/nearhost", Env: []EnvVar{{"PATH", "/usr/bin"}}, LogPath: "/home/e/.local/state/nearhost/core.log", Graphical: true}
}
