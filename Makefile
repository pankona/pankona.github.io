all:
ifdef post
	hugo --panicOnWarning new posts/$(post).md
else
	hugo --panicOnWarning
endif

serve:
	hugo --panicOnWarning server -D --disableFastRender

edit:
ifndef EDITOR
	EDITOR=vim
endif
	@cd $(CURDIR)/content/posts; $(EDITOR) `ls -t | peco`
