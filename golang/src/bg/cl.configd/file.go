/*
 * COPYRIGHT 2019 Brightgate Inc.  All rights reserved.
 *
 * This copyright notice is Copyright Management Information under 17 USC 1202
 * and is included to protect this work and deter copyright infringement.
 * Removal or alteration of this Copyright Management Information without the
 * express written permission of Brightgate Inc is prohibited, and any
 * such unauthorized removal or alteration will be a violation of federal law.
 */

package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"

	"bg/common/cfgtree"
)

type fileStore struct {
	dir string
}

func (store *fileStore) String() string {
	return store.dir
}

func (store *fileStore) get(ctx context.Context, uuid string) (*cfgtree.PTree, error) {
	slog.Infof("Loading state for %s from file", uuid)

	path := store.dir + "/" + uuid + ".json"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		slog.Warnf("No config file at %s", path)
		return nil, fmt.Errorf("no such site: %s", uuid)
	}

	file, err := ioutil.ReadFile(path)
	if err != nil {
		slog.Warnf("Failed to load %s: %v\n", path, err)
		return nil, err
	}

	tree, err := cfgtree.NewPTree("@/", file)
	if err != nil {
		err = fmt.Errorf("importing %s: %v", path, err)
	}

	return tree, err
}

// set is not implemented for fileStore; it is primarily designed for
// development activities.
func (store *fileStore) set(ctx context.Context, uuid string, tree *cfgtree.PTree) error {
	return nil
}

func newFileStore(dir string) (configStore, error) {
	var store configStore = &fileStore{dir}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		slog.Warnf("Store directory %s does not exist", dir)
		return nil, err
	}
	return store, nil
}
