GOMD2MAN ?= $(shell command -v go-md2man || echo '$(GOBIN)/go-md2man')
PREFIX ?= ${DESTDIR}/usr
MANINSTALLDIR=${PREFIX}/share/man
MANPAGES_MD = $(wildcard docs/*.5.md)
MANPAGES ?= $(MANPAGES_MD:%.md=%)

.PHONY: docs
docs: $(patsubst %.md,%.1,$(filter-out %.5.md,$(wildcard *.md))) containers-storage.conf.5

%.1: %.md ../../hack/md2man-preprocess.sed
	sed -E -f ../../hack/md2man-preprocess.sed $< | $(GOMD2MAN) -out $@

containers-storage.conf.5: containers-storage.conf.5.md ../../hack/md2man-preprocess.sed
	sed -E -f ../../hack/md2man-preprocess.sed $< | $(GOMD2MAN) -out $@

.PHONY: install
install:
	install -d -m 755 ${MANINSTALLDIR}/man5
	install -m 644 *.5 ${MANINSTALLDIR}/man5/
