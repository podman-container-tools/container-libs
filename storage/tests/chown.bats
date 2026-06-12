#!/usr/bin/env bats

load helpers

@test "chown-preserves-modtimes" {
	# This test needs "tar".
	if test -z "$(which tar 2> /dev/null)" ; then
		skip "need tar"
	fi
	if test -z "$(which dd 2> /dev/null)" ; then
		skip "need dd"
	fi
	if [[ "${STORAGE_OPTION}" =~ "fuse-overlayfs" ]] ; then
		# this test would be tripped up by https://github.com/containers/fuse-overlayfs/issues/400
		skip "not testing with fuse-overlayfs"
	fi

	# Create a tree for a layer with at least one hard link and some directories.
	pushd $TESTDIR > /dev/null
	mkdir layer layer/layer1 layer/layer1/directory layer/layer1/directory/subdirectory
	createrandom layer/layer1/directory/subdirectory/linktarget
	ln layer/layer1/directory/subdirectory/linktarget layer/layer1/directory/subdirectory/link
	ln -s linktarget layer/layer1/directory/subdirectory/symlink
	createrandom layer/layer1/directory/subdirectory/otherfile
	touch -d 1970-01-01T00:00:00Z layer/layer1 layer/layer1/directory layer/layer1/directory/subdirectory
	# Create another tree with just the directories.
	mkdir layer/layer2 layer/layer2/directory layer/layer2/directory/subdirectory
	touch -d 1970-01-01T00:00:00Z layer/layer2 layer/layer2/directory layer/layer2/directory/subdirectory
	popd > /dev/null

	# Create a temporary layer.
	run storage --debug=false create-layer
	echo "$output"
	[ "$status" -eq 0 ]
	[ "$output" != "" ]
	templayer="$output"

	# Copy the content into it.
	run storage --debug=false copy --chown 0:0 $TESTDIR/layer/layer1 "$templayer":/
	echo "$output"
	[ "$status" -eq 0 ]
	[ "$output" == "" ]

	# Generate a diff with the contents.
	run storage --debug=false diff -f $TESTDIR/layer1.tar "$templayer"
	echo "$output"
	[ "$status" -eq 0 ]
	[ "$output" == "" ]

	# Create another temporary layer.
	run storage --debug=false create-layer
	echo "$output"
	[ "$status" -eq 0 ]
	[ "$output" != "" ]
	templayer="$output"

	# Copy the content into it.
	run storage --debug=false copy --chown 0:0 $TESTDIR/layer/layer2 "$templayer":/
	echo "$output"
	[ "$status" -eq 0 ]
	[ "$output" == "" ]

	# Generate a diff with the contents.
	run storage --debug=false diff -f $TESTDIR/layer2.tar "$templayer"
	echo "$output"
	[ "$status" -eq 0 ]
	[ "$output" == "" ]

	# Create a new layer using first diff to populate it.
	run storage --debug=false import-layer --name lower --file $TESTDIR/layer1.tar --uidmap 0:2:1024 --gidmap 0:2:1024
	echo "$output"
	[ "$status" -eq 0 ]
	[ "$output" != "" ]

	# Create a new layer based on the one we just created, overwriting some
	# of its directories that will also contain items that are chown'd when
	# being pulled up after the directories are extracted onto disk.
	run storage --debug=false import-layer --name middle --file $TESTDIR/layer2.tar --uidmap 0:2:1024 --gidmap 0:2:1024 lower
	echo "$output"
	[ "$status" -eq 0 ]
	[ "$output" != "" ]

	# And another one with a different ID map.
	run storage --debug=false import-layer --name upper --file $TESTDIR/layer2.tar --uidmap 0:3:1024 --gidmap 0:3:1024 middle
	echo "$output"
	[ "$status" -eq 0 ]
	[ "$output" != "" ]

	# Create an image using that layer as its top layer.
	run storage --debug=false create-image --name image upper
	echo "$output"
	[ "$status" -eq 0 ]
	[ "$output" != "" ]

	# Create a container using that image.
	run storage --debug=false create-container --name container --hostuidmap --hostgidmap image
	echo "$output"
	[ "$status" -eq 0 ]
	[ "$output" != "" ]

	# Check for inconsistencies.
	run storage --debug=false check
	echo "$output"
	[ "$status" -eq 0 ]
	[ "$output" == "" ]
}
