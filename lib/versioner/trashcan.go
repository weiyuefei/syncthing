// Copyright (C) 2015 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package versioner

import (
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"github.com/syncthing/syncthing/lib/fs"
	"github.com/syncthing/syncthing/lib/osutil"
)

func init() {
	// Register the constructor for this type of versioner
	Factories["trashcan"] = NewTrashcan
}

type Trashcan struct {
	filesystem   fs.Filesystem
	cleanoutDays int
	stop         chan struct{}
}

func NewTrashcan(folderID string, filesystem fs.Filesystem, params map[string]string) Versioner {
	cleanoutDays, _ := strconv.Atoi(params["cleanoutDays"])
	// On error we default to 0, "do not clean out the trash can"

	s := &Trashcan{
		filesystem:   filesystem,
		cleanoutDays: cleanoutDays,
		stop:         make(chan struct{}),
	}

	l.Debugf("instantiated %#v", s)
	return s
}

// Archive moves the named file away to a version archive. If this function
// returns nil, the named file does not exist any more (has been archived).
func (t *Trashcan) Archive(filePath string) error {
	info, err := t.filesystem.Lstat(filePath)
	if fs.IsNotExist(err) {
		l.Debugln("not archiving nonexistent file", filePath)
		return nil
	} else if err != nil {
		return err
	}
	if info.IsSymlink() {
		panic("bug: attempting to version a symlink")
	}

	versionsDir := ".stversions"
	if _, err := t.filesystem.Stat(versionsDir); err != nil {
		if !fs.IsNotExist(err) {
			return err
		}

		l.Debugln("creating versions dir", versionsDir)
		if err := t.filesystem.MkdirAll(versionsDir, 0777); err != nil {
			return err
		}
		t.filesystem.Hide(versionsDir)
	}

	l.Debugln("archiving", filePath)

	archivedPath := filepath.Join(versionsDir, filePath)
	if err := t.filesystem.MkdirAll(filepath.Dir(archivedPath), 0777); err != nil && !fs.IsExist(err) {
		return err
	}

	l.Debugln("moving to", archivedPath)

	if err := osutil.Rename(t.filesystem, filePath, archivedPath); err != nil {
		return err
	}

	// Set the mtime to the time the file was deleted. This is used by the
	// cleanout routine. If this fails things won't work optimally but there's
	// not much we can do about it so we ignore the error.
	t.filesystem.Chtimes(archivedPath, time.Now(), time.Now())

	return nil
}

func (t *Trashcan) Serve() {
	l.Debugln(t, "starting")
	defer l.Debugln(t, "stopping")

	// Do the first cleanup one minute after startup.
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()

	for {
		select {
		case <-t.stop:
			return

		case <-timer.C:
			if t.cleanoutDays > 0 {
				if err := t.cleanoutArchive(); err != nil {
					l.Infoln("Cleaning trashcan:", err)
				}
			}

			// Cleanups once a day should be enough.
			timer.Reset(24 * time.Hour)
		}
	}
}

func (t *Trashcan) Stop() {
	close(t.stop)
}

func (t *Trashcan) String() string {
	return fmt.Sprintf("trashcan@%p", t)
}

func (t *Trashcan) cleanoutArchive() error {
	versionsDir := ".stversions"
	if _, err := t.filesystem.Lstat(versionsDir); fs.IsNotExist(err) {
		return nil
	}

	cutoff := time.Now().Add(time.Duration(-24*t.cleanoutDays) * time.Hour)
	currentDir := ""
	filesInDir := 0
	walkFn := func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			// We have entered a new directory. Lets check if the previous
			// directory was empty and try to remove it. We ignore failure for
			// the time being.
			if currentDir != "" && filesInDir == 0 {
				t.filesystem.Remove(currentDir)
			}
			currentDir = path
			filesInDir = 0
			return nil
		}

		if info.ModTime().Before(cutoff) {
			// The file is too old; remove it.
			t.filesystem.Remove(path)
		} else {
			// Keep this file, and remember it so we don't unnecessarily try
			// to remove this directory.
			filesInDir++
		}
		return nil
	}

	if err := t.filesystem.Walk(versionsDir, walkFn); err != nil {
		return err
	}

	// The last directory seen by the walkFn may not have been removed as it
	// should be.
	if currentDir != "" && filesInDir == 0 {
		t.filesystem.Remove(currentDir)
	}
	return nil
}
