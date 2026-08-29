//go:build !darwin

package main

// Only macOS ties a permission grant to a binary's signature, so everywhere
// else this is a no-op that returns nothing to say.
func signSelf(string) string { return "" }
