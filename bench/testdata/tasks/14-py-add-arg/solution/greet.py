import argparse


def main():
    parser = argparse.ArgumentParser(description="Greet someone.")
    parser.add_argument("name")
    parser.add_argument("--count", type=int, default=1)
    args = parser.parse_args()
    for _ in range(args.count):
        print(f"Hello, {args.name}!")


if __name__ == "__main__":
    main()
