import typing
import dataclasses
import json
import csv
import html2text
import opencc
import sys



_diacritics_trans = str.maketrans({
    'á': 'aa',
    'ó': 'o',
    'ú': 'oo',
    'ü': 'ui',
    'û': 'u',
    'ö': 'oe',
    '\'': 'h',
})

def replace_diacritics(reading):
    return reading.translate(_diacritics_trans)


class EnhancedJSONEncoder(json.JSONEncoder):
    def default(self, o):
        if dataclasses.is_dataclass(o):
            return dataclasses.asdict(o)
        return super().default(o)


@dataclasses.dataclass
class Definition:
    readings: typing.List[str]
    readings_no_diacritics: typing.List[str]
    meanings: typing.List[str]


@dataclasses.dataclass
class Entry:
    word: str
    simplified: typing.List[str]
    source_code: str
    definitions: typing.List[Definition]


def group_lines(lines):
    current_group = []
    groups = [current_group]
    for r in lines:
        if r == '':
            if current_group:
                current_group = []
                groups.append(current_group)
            continue

        current_group.append(r)

    if groups[-1] == []:
        groups.pop()

    return groups


out = []

text_maker = html2text.HTML2Text()
text_maker.ignore_emphasis = True
text_maker.body_width = None

def safe_split(s):
    in_paren = 0
    cur = []
    parts = [cur]

    for c in s:
        if c == '(':
            in_paren += 1
            cur.append(c)
            continue

        if c == ')':
            in_paren -= 1
            cur.append(c)
            continue

        if c == ',' and in_paren == 0:
            cur = []
            parts.append(cur)
            continue

        cur.append(c)

    return [''.join(p).strip() for p in parts]

occ = opencc.OpenCC('t2s')


with open('dict.txt') as f:
    rd = csv.reader(f, delimiter="\t", quotechar='"')
    for row in rd:
        word, rest = row

        raw_readings, raw_meanings = rest.split('<hr>')
        word = text_maker.handle(word).strip()
        simplified = occ.convert(word)

        if len(word) > 1:
            print('WEIRD', word, file=sys.stderr)

        readings =  [r.strip() for r in text_maker.handle(raw_readings.replace('<font', '*<font')).replace('\n\n', '\n').strip().split('\n')]
        meanings = [r.strip() for r in text_maker.handle(raw_meanings).replace('\n\n', '\n').strip().split('\n')]

        # Simple case
        if not any('(' in r for r in readings):
            # this case is easy!
            readings = sorted([r for r in readings if r])
            entry = Entry(word, [simplified], 'c', [Definition(readings, [replace_diacritics(reading) for reading in readings], [c for r in meanings if r for c in safe_split(r)])])
            out.append(entry)
            continue

        # Multiple readings
        if readings[0][0] == '(':
            if any('(' in r for r in readings[1:]):
                # handle this later
                print('SKIPPED', word, readings, file=sys.stderr)
                continue

            if readings[0].lower() == '(two words)' or readings[0].lower() == '(two different words)':
                expected_groups = 2
            elif readings[0].lower() == '(three words)' or readings[0].lower() == '(3 words)':
                expected_groups = 3
            elif readings[0].lower() == '(four words)':
                expected_groups = 4
            elif readings[0].lower() == '(five words)':
                expected_groups = 5
            elif 'both' in readings[0].lower():
                expected_groups = 1
            else:
                print('SKIPPED', word, readings, file=sys.stderr)

            # reading groups
            reading_groups = group_lines(readings[1:])
            if expected_groups == 1:
                reading_groups = [[r for g in reading_groups for r in g]]

            if sum(len(g) for g in reading_groups) == expected_groups:
                reading_groups = [[r] for g in reading_groups for r in g]

            if len(reading_groups) == 1 and len(reading_groups[0]) == 1:
                reading_groups = [[reading_groups[0][0]] for _ in range(expected_groups)]

            if len(reading_groups) != expected_groups:
                print('UNKNOWN', word, reading_groups, f'{expected_groups} expected reading groups', file=sys.stderr)
                continue

            # meaning groups
            meaning_groups = group_lines(meanings)
            if expected_groups == 1:
                meaning_groups = [[r for g in meaning_groups for r in g]]

            if sum(len(g) for g in meaning_groups) == expected_groups:
                meaning_groups = [[r] for g in meaning_groups for r in g]

            if len(meaning_groups) == 1 and len(meaning_groups[0]) == 1:
                meaning_groups = [[meaning_groups[0][0]] for _ in range(expected_groups)]

            if len(meaning_groups) != expected_groups:
                print('UNKNOWN', word, meaning_groups, f'{expected_groups} expected meaning groups', file=sys.stderr)
                continue

            meaning_groups = [[w for c in g for w in safe_split(c)] for g in meaning_groups]

            if len(reading_groups) != len(meaning_groups):
                raise Exception

            entry = Entry(word, [simplified], 'c', [Definition(sorted(rs), [replace_diacritics(reading) for reading in sorted(rs)], ms) for rs, ms in zip(reading_groups, meaning_groups)])
            out.append(entry)

            continue

        # Multiple readings, funny handling
        print('SKIPPED', word, readings, file=sys.stderr)


with open('dict.ndjson', 'a') as f:
    for entry in out:
        json.dump(entry, f, ensure_ascii=False, cls=EnhancedJSONEncoder)
        f.write('\n')
