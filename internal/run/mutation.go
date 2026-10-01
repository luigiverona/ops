package run

// CheckMutation allows mutation helpers to recheck ownership immediately before
// persistent in-process writes, including after a concurrent sudo refresh fails.
// Injected runners may provide their own gate; the application also requires an
// explicit ownership capability before entering its approved mutation phase.
func CheckMutation(r Runner) error {
	if guard, ok := r.(interface{ CheckMutation() error }); ok {
		return guard.CheckMutation()
	}
	return nil
}

func (e Exec) CheckMutation() error {
	if e.Owner == nil {
		return nil
	}
	return e.Owner.Check()
}

// Mutate serializes a short persistent file change with ownership poisoning.
// Do not call Runner or perform long/network work inside f: commands acquire the
// same admission lock. A mutation already in progress finishes before poisoning;
// no later file operation can pass the poisoned gate.
func Mutate(r Runner, f func() error) error {
	if guard, ok := r.(interface{ Mutate(func() error) error }); ok {
		return guard.Mutate(f)
	}
	return f()
}
func (e Exec) Mutate(f func() error) error {
	if e.Owner == nil {
		return f()
	}
	e.Owner.mu.Lock()
	defer e.Owner.mu.Unlock()
	if err := e.Owner.checkLocked(); err != nil {
		return err
	}
	return f()
}
