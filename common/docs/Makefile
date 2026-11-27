PREFIX := /usr/local
DATADIR := ${PREFIX}/share
MANDIR := $(DATADIR)/man
GOMD2MAN ?= $(shell command -v go-md2man || echo '$(GOBIN)/go-md2man')

docs: $(patsubst %.md,%,$(wildcard *.md))

%.5:  %.5.md ../../hack/md2man-preprocess.sed
	sed -E -f ../../hack/md2man-preprocess.sed $< | $(GOMD2MAN) -out $@

.PHONY: install
install:
	install -d ${DESTDIR}/${MANDIR}/man5
	install -m 0644 *5 ${DESTDIR}/${MANDIR}/man5
	install -m 0644 links/.*.5 ${DESTDIR}/${MANDIR}/man5

.PHONY: clean
clean:
	$(RM) *.5
