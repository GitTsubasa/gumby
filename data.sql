set client_encoding to 'UTF8';

begin;
create temporary table t (
    j jsonb
);
\copy t from 'dict.ndjson' with (format csv, quote '|', delimiter E'\t');
create temporary view v as
select
    (j ->> 'word') word,
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
insert into meanings (definition_id, meaning, meaning_index_col)
select
    (
        select
            id
        from
            definitions
        where
            word = v.word
            and readings = v.readings),
    meaning,
    to_tsvector(meaning)
from
    v,
    unnest(v.meanings) meaning
where
    v.readings != '{}';
--
commit;

