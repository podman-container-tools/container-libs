#!/usr/bin/env bats

load helpers

@test "applydiff" {
	# The checkdiffs function needs "tar".
	if test -z "$(which tar 2> /dev/null)" ; then
		skip "need tar"
	fi

	# Create and populate three interesting layers.
	populate

	# Extract the layers.
	storage diff -u -f $TESTDIR/lower.tar $lowerlayer
	storage diff -c -f $TESTDIR/middle.tar $midlayer
	storage diff -u -f $TESTDIR/upper.tar $upperlayer

	# Delete the layers.
	storage delete-layer $upperlayer
	storage delete-layer $midlayer
	storage delete-layer $lowerlayer

	# Create new layers and populate them using the layer diffs.
	run storage --debug=false create-layer
	[ "$status" -eq 0 ]
	[ "$output" != "" ]
	lowerlayer="$output"
	storage applydiff -f $TESTDIR/lower.tar "$lowerlayer"

	run storage --debug=false create-layer "$lowerlayer"
	[ "$status" -eq 0 ]
	[ "$output" != "" ]
	midlayer="$output"
	storage applydiff -f $TESTDIR/middle.tar "$midlayer"

	run storage --debug=false create-layer "$midlayer"
	[ "$status" -eq 0 ]
	[ "$output" != "" ]
	upperlayer="$output"
	storage applydiff -f $TESTDIR/upper.tar "$upperlayer"

	# The contents of these new layers should match what the old ones had.
	checkchanges
	checkdiffs
}

@test "apply-diff-from-staging-directory" {
	case "$STORAGE_DRIVER" in
	overlay*)
		;;
	*)
		skip "driver $STORAGE_DRIVER does not support diff-from-staging-directory"
		;;
	esac

	SRC=$TESTDIR/source
	mkdir -p $SRC
	createrandom $SRC/file1
	createrandom $SRC/file2
	createrandom $SRC/file3

	local sconf=$TESTDIR/storage.conf

	local root=`storage status 2>&1 | awk '/^Root:/{print $2}'`
	local runroot=`storage status 2>&1 | awk '/^Run Root:/{print $3}'`

	cat >$sconf <<EOF
[storage]
driver="overlay"
graphroot="$root"
runroot="$runroot"

[storage.options.pull_options]
enable_partial_images = "true"
EOF

	# Create a layer.
	CONTAINERS_STORAGE_CONF=$sconf run ${STORAGE_BINARY} create-layer
	[ "$status" -eq 0 ]
	[ "$output" != "" ]
	layer="$output"

	CONTAINERS_STORAGE_CONF=$sconf run ${STORAGE_BINARY} applydiff-using-staging-dir $layer $SRC
	[ "$status" -eq 0 ]

	name=safe-image
	CONTAINERS_STORAGE_CONF=$sconf run ${STORAGE_BINARY} create-image --name $name $layer
	[ "$status" -eq 0 ]

	ctrname=foo
	CONTAINERS_STORAGE_CONF=$sconf run ${STORAGE_BINARY} create-container --name $ctrname $name
        [ "$status" -eq 0 ]

	CONTAINERS_STORAGE_CONF=$sconf run ${STORAGE_BINARY} mount $ctrname
	[ "$status" -eq 0 ]
	mount="$output"

	for i in file1 file2 file3 ; do
		run cmp $SRC/$i $mount/$i
		[ "$status" -eq 0 ]
	done
}

@test "apply-implicitdir-diff" {
	case "$STORAGE_DRIVER" in
	overlay*)
		;;
	*)
		skip "driver $STORAGE_DRIVER does not support the affected overlay path"
		;;
	esac

	if test -z "$(which tar 2> /dev/null)" ; then
		skip "need tar"
	fi

	pushd "$TESTDIR" >/dev/null
	mkdir subdirectory1
	chmod 0700 subdirectory1
	mkdir subdirectory2
	chmod 0750 subdirectory2
	tar cvf lower.tar subdirectory1 subdirectory2
	touch subdirectory1/testfile1 subdirectory2/testfile2
	tar cvf middle.tar subdirectory1/testfile1 subdirectory2/testfile2
	popd >/dev/null

	run storage --debug=false create-layer
	[ "$status" -eq 0 ]
	[ "$output" != "" ]
	lowerlayer="$output"
	storage applydiff -f "$TESTDIR/lower.tar" "$lowerlayer"

	run storage --debug=false create-layer "$lowerlayer"
	[ "$status" -eq 0 ]
	[ "$output" != "" ]
	middlelayer="$output"
	storage applydiff -f "$TESTDIR/middle.tar" "$middlelayer"

	run storage --debug=false create-layer "$middlelayer"
	[ "$status" -eq 0 ]
	[ "$output" != "" ]
	upperlayer="$output"

	run storage --debug=false mount "$upperlayer"
	[ "$status" -eq 0 ]
	[ "$output" != "" ]
	mountpoint="$output"

	run stat -c %a "$TESTDIR/subdirectory1"
	[ "$status" -eq 0 ]
	expected="$output"

	run stat -c %a "$mountpoint/subdirectory1"
	[ "$status" -eq 0 ]
	actual="$output"
	[ "$actual" = "$expected" ]

	run stat -c %a "$TESTDIR/subdirectory2"
	[ "$status" -eq 0 ]
	expected="$output"

	run stat -c %a "$mountpoint/subdirectory2"
	[ "$status" -eq 0 ]
	actual="$output"
	[ "$actual" = "$expected" ]
}
