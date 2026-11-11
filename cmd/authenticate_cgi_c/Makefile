TARGET := ../../embed/authenticate_cgi/authenticate_cgi_linux_

build:: build_static

build_static::
	gcc main.c -o $(TARGET)amd64 -static -O2 -s
	aarch64-linux-gnu-gcc main.c -o $(TARGET)arm64 -static -O2 -s
