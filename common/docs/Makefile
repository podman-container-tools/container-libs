PREFIX := /usr/local
MAN_INSTALL_DIR := $(PREFIX)/share/man/man5

MANPAGES := $(patsubst %.md,%,$(wildcard *.md))
MAN_LINKS := $(shell ls -A links/ 2>/dev/null | sed 's|^|links/|')

include ../../hack/man-docs.mk
