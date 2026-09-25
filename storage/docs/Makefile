MAN_INSTALL_DIR=/usr/share/man/man5

MANPAGES := $(patsubst %.5.md,%.5,$(wildcard *.5.md)) $(patsubst %.md,%.1,$(filter-out %.5.md,$(wildcard *.md)))

include ../../hack/man-docs.mk
