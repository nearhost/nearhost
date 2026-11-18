package config

// Update changes the config at path under a lock that every other Update
// takes too, in this process or another nearhost: it loads the file, calls
// change on what it read, validates and saves, then unlocks. change must be
// quick and ask nothing of the user, since every other writer waits on it;
// it reapplies a change already decided to the config as the file has it
// now. When change fails, or leaves the config invalid, nothing is saved.
// Nor is anything saved, and change is not called, while the file holds a
// key Nearhost does not know, which the save would delete: the error is an
// *UnknownKeysError naming each key and its line.
//
// The lock is on a file beside the config, path + ".lock", which is kept
// after use: removing it would let a writer that opened it before the
// removal and one that opens it after hold the lock at once.
func Update(path string, change func(*Config) error) error {
	unlock, err := lockFile(path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	cfg, data, err := load(path)
	if err != nil {
		return err
	}
	// Save writes only what the structs hold, so it would delete a key
	// Nearhost does not know. Refuse instead, and name every such key. The
	// keys are looked for in the bytes just loaded, so a hand edit saved
	// meanwhile cannot be checked in place of the version being changed.
	if data != nil {
		unknown, err := unknownKeysIn(path, data)
		if err != nil {
			return err
		}
		if len(unknown) > 0 {
			return &UnknownKeysError{Path: path, Keys: unknown}
		}
	}
	if err := change(cfg); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	return Save(path, cfg)
}
