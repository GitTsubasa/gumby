import json
import opencc
import os
import sys

occ = opencc.OpenCC('t2s')


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


for fn in os.listdir('dictionaries'):
    source, ext = os.path.splitext(fn)

    with open(os.path.join('dictionaries', fn), 'r') as f:
        for line in f:
            l = json.loads(line)
            json.dump({
                'word': l['word'],
                'simplified': [occ.convert(l['word'])],
                'source': source,
                'definitions': [{
                    'readings': d['readings'],
                    'readings_no_diacritics': [replace_diacritics(r) for r in d['readings']],
                    'meanings': d['meanings'],
                } for d in l['definitions']]
            }, sys.stdout, ensure_ascii=False)
            sys.stdout.write('\n')
