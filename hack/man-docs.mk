# Shared man page build rules
#
# Shared logic for building man pages from markdown sources using go-md2man.
# Include this file from your docs/Makefile after setting following variables
# before include.
#
# - MANPAGES: list of man page targets ("foo.5 bar.5 ...")
# - MAN_INSTALL_DIR: installation path under DESTDIR ("/usr/share/man/man5")
#
# Optional variables:
# - MAN_LINKS: additional .so link files or pre-built man pages to install
#
# Targets provided can then be used in top-level Makefile:
# - docs: build all man pages from their .md sources
# - install: install man pages (and links) into DESTDIR$(MAN_INSTALL_DIR)
# - clean: remove built man pages

GOMD2MAN ?= $(shell command -v go-md2man || echo '$(GOBIN)/go-md2man')

# Pattern rules: convert .5.md -> .5, .1.md -> .1
%.5: %.5.md
	$(GOMD2MAN) -in $< -out $@

%.1: %.1.md
	$(GOMD2MAN) -in $< -out $@

# Catch-all for .md without section suffix (e.g., storage command pages)
%.1: %.md
	$(GOMD2MAN) -in $< -out $@

.PHONY: docs
docs: $(MANPAGES)

.PHONY: install
install: $(MANPAGES)
	install -d -m 755 $(DESTDIR)$(MAN_INSTALL_DIR)
	install -m 644 $(MANPAGES) $(DESTDIR)$(MAN_INSTALL_DIR)/
ifneq ($(MAN_LINKS),)
	install -m 644 $(MAN_LINKS) $(DESTDIR)$(MAN_INSTALL_DIR)/
endif

.PHONY: clean
clean:
	rm -f $(MANPAGES)
