set client_encoding to 'UTF8';

begin;
create temporary table t (
    j jsonb
);
\copy t from 'dict.ndjson' with (format csv, quote '|', delimiter E'\t');
create temporary view v as
select
    (j ->> 'word') word,
    (j ->> 'simplified_guess') simplified_guess,
    ((
        select
            array_agg(m)
        from jsonb_array_elements_text(ds -> 'readings') m)) readings,
    ((
        select
            array_agg(m)
        from jsonb_array_elements_text(ds -> 'meanings') m)) meanings
from
    t,
    jsonb_array_elements(j -> 'definitions') ds;
delete from word_fts_tsvectors;
delete from meanings;
delete from definitions;
delete from words;
--
insert into words (word)
select
    word
from
    v
group by
    word;
--
insert into definitions (word, readings)
select
    word,
    coalesce(readings, array[]::text[])
from
    v
group by
    word,
    readings;
--
insert into meanings (definition_id, meaning)
select
    (
        select
            id
        from
            definitions
        where
            word = v.word
            and readings = v.readings),
    meaning
from
    v,
    unnest(v.meanings) meaning
where
    v.readings != '{}';
-- insert english meanings
insert into word_fts_tsvectors (word, tsvector)
select
    v.word,
    to_tsvector('english_nostop', meaning)
from
    v,
    unnest(v.meanings) meaning
on conflict (word,
    tsvector)
    do nothing;
-- insert plain words
insert into word_fts_tsvectors (word, tsvector)
select
    v.word,
    to_tsvector('english_nostop', v.word)
from
    v,
    unnest(v.meanings) meaning
on conflict (word,
    tsvector)
    do nothing;
-- insert simplified forms
insert into word_fts_tsvectors (word, tsvector)
select
    v.word,
    to_tsvector('english_nostop', v.simplified_guess)
from
    v,
    unnest(v.meanings) meaning
on conflict (word,
    tsvector)
    do nothing;
commit;

