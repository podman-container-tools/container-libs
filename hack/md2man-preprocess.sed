# Filter Markdown man page sources before passing them to go-md2man.
# Use as: sed -E -f hack/md2man-preprocess.sed foo.5.md | go-md2man -out foo.5
#
# Links to other man pages in this repository (and to sections of the same
# page) make the Markdown easy to navigate on GitHub, but man pages have no
# link mechanism, and go-md2man would render the link target as extra text.
# So convert relative links to their plain link text:
#
#     [containers.conf(5)](containers.conf.5.md)  ->  containers.conf(5)
#     [below](#criticaltype)                      ->  below
#
# Links with a URL scheme (e.g. https://...) are kept as they are.
s/\[([^]]*)\]\([^):]*\)/\1/g
