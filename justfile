set positional-arguments

default:
	just run

clean:
    rm -f gb

build: clean
    CGO_ENABLED=0 go build -o "gb" .

run *args='main.go': build
    ./gb "$@"
