import csv
import re
import json
import itertools
import html2text
import sys


text_maker = html2text.HTML2Text()
text_maker.ignore_emphasis = True
text_maker.body_width = None

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


def dump(word, readings, meanings):
    json.dump({'word': word, 'definitions': [{'readings': readings, 'meanings': meanings}]}, sys.stdout, ensure_ascii=False)
    sys.stdout.write('\n')


with open('qianplus.txt', 'r') as f:
    rd = csv.reader(f, delimiter="\t", quotechar='"')

    for row in rd:
        both, raw_meanings = row
        both = text_maker.handle(both).strip().split('\n')
        both = [l.strip() for l in both if l.strip()]

        readings_i = 0
        for i, word in enumerate(both):
            if re.match(r'[a-z]', replace_diacritics(word[0])):
                readings_i = i
                break

        words = both[:i]
        readings = both[i:]

        raw_meanings = text_maker.handle(raw_meanings).strip().split('\n')
        raw_meanings = [l.strip() for l in raw_meanings if l.strip() and l.strip() != '* * *']

        if raw_meanings[0].startswith('a.'):
            meanings_groups = []

            # has multiple meaning groups
            for l in raw_meanings:
                n = chr(ord('a') + len(meanings_groups))
                if l.startswith(f'{n}.'):
                    meanings_groups.append([])
                m = meanings_groups[-1]
                l = re.sub(r'^(\d+\\|[a-z])\.', '', l).strip()
                if l:
                    m.append(l)

            if len(words) != len(meanings_groups):
                if len(words) == 1:
                    meanings_groups = [[meaning for meanings in meanings_groups for meaning in meanings]]
                else:
                    print(f'WEIRD: {words} {meanings_groups}', file=sys.stderr)

            for word, meanings in zip(words, meanings_groups):
                dump(word, readings, meanings)
        else:
            if len(words) > 1:
                if len(words) == len(raw_meanings):
                    for word, meanings in zip(word, raw_meanings):
                        meanings = [re.sub(r'^\d+\\\. ', '', m) for m in meanings]
                        dump(word, readings, meanings)
                elif len(raw_meanings) == 1:
                    for word in words:
                        meanings = [re.sub(r'^\d+\\\. ', '', m) for m in raw_meanings]
                        dump(word, readings, meanings)
                else:
                    raise Exception(words, raw_meanings)
            elif not words:
                raise Exception(readings, raw_meanings)
            else:
                meanings = [re.sub(r'^\d+\\\. ', '', m) for m in raw_meanings]
                dump(words[0], readings, meanings)
