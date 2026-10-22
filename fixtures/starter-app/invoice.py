import csv
import sys


def total_cents(path: str) -> int:
    with open(path, newline='') as handle:
        return sum(int(row['hours']) * int(row['rate_cents']) for row in csv.DictReader(handle))


if __name__ == '__main__':
    print(total_cents(sys.argv[1]))
