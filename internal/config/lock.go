package config

// Update changes the config at path under a lock that every other Update
// takes too, in this process or another nearhost: it loads the file, calls
// change on what it read, validates and saves, then unlocks. change must be
// quick and ask nothing of the user, since every other writer waits on it;
// it reapplies a change already decided to the config as the file has it
// now. When change fails, or leaves the config invalid, nothing is saved.
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
	cfg, err := Load(path)
	if err != nil {
		return err
	}
	if err := change(cfg); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	return Save(path, cfg)
}
