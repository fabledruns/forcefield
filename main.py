# main.py
# Writes 1000 lines to test.txt

OUTPUT_FILE = "test.txt"
NUM_LINES = 1000


def main() -> None:
    with open(OUTPUT_FILE, "w", encoding="utf-8") as f:
        for i in range(1, NUM_LINES + 1):
            f.write(f"Line {i}\n")


if __name__ == "__main__":
    main()
