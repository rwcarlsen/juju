// Copyright 2013 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package utils

import (
	"fmt"
	"io"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"regexp"
	"runtime"

	"launchpad.net/juju-core/juju/osenv"
)

// UserHomeDir returns the home directory for the specified user, or the
// home directory for the current user if the specified user is empty.
func UserHomeDir(userName string) (homeDir string, err error) {
	var u *user.User
	if userName == "" {
		// TODO (wallyworld) - fix tests on Windows
		// Ordinarily, we'd always use user.Current() to get the current user
		// and then get the HomeDir from that. But our tests rely on poking
		// a value into $HOME in order to override the normal home dir for the
		// current user. So on *nix, we're forced to use osenv.Home() to make
		// the tests pass. All of our tests currently construct paths with the
		// default user in mind eg "~/foo".
		if runtime.GOOS == "windows" {
			u, err = user.Current()
		} else {
			return osenv.Home(), nil
		}
	} else {
		u, err = user.Lookup(userName)
		if err != nil {
			return "", err
		}
	}
	return u.HomeDir, nil
}

var userHomePathRegexp = regexp.MustCompile("(~(?P<user>[^/]*))(?P<path>.*)")

// NormalizePath expands a path containing ~ to its absolute form,
// and removes any .. or . path elements.
func NormalizePath(dir string) (string, error) {
	if userHomePathRegexp.MatchString(dir) {
		user := userHomePathRegexp.ReplaceAllString(dir, "$user")
		userHomeDir, err := UserHomeDir(user)
		if err != nil {
			return "", err
		}
		dir = userHomePathRegexp.ReplaceAllString(dir, fmt.Sprintf("%s$path", userHomeDir))
	}
	return filepath.Clean(dir), nil
}

// JoinServerPath joins any number of path elements into a single path, adding
// a path separator (based on the current juju server OS) if necessary. The
// result is Cleaned; in particular, all empty strings are ignored.
func JoinServerPath(elem ...string) string {
	return path.Join(elem...)
}

// UniqueDirectory returns "path/name" if that directory doesn't exist.  If it
// does, the method starts appending .1, .2, etc until a unique name is found.
func UniqueDirectory(path, name string) (string, error) {
	dir := filepath.Join(path, name)
	_, err := os.Stat(dir)
	if os.IsNotExist(err) {
		return dir, nil
	}
	for i := 1; ; i++ {
		dir := filepath.Join(path, fmt.Sprintf("%s.%d", name, i))
		_, err := os.Stat(dir)
		if os.IsNotExist(err) {
			return dir, nil
		} else if err != nil {
			return "", err
		}
	}
}

// CopyFile writes the contents of the given source file to dest.
func CopyFile(dest, source string) error {
	df, err := os.Create(dest)
	if err != nil {
		return err
	}
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(df, f)
	return err
}

// IsDirectory is a simple helper method to determine if a path points at a
// directory.
func IsDirectory(path string) bool {
	fileInfo, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		return false
	}
	return fileInfo.IsDir()
}
