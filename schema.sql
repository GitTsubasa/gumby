create text search dictionary english_stem_nostop (
    template = snowball,
    language =
    english
);

create text search configuration english_nostop (
    copy = pg_catalog.english
);

alter text search configuration english_nostop
    alter mapping for asciiword, asciihword, hword_asciipart, hword, hword_part, word with english_stem_nostop;

create table words (
    word text not null primary key
);

create index on words (word text_pattern_ops);

create table word_fts_tsvectors (
    word text not null references words (word),
    tsvector tsvector,
    primary key (word, tsvector)
);

create index on word_fts_tsvectors using gin (tsvector);

create table definitions (
    id bigserial primary key,
    word text not null references words (word),
    readings text[] not null
);

create unique index on definitions (word, readings);

create table meanings (
    id bigserial primary key,
    definition_id bigint not null references definitions (id),
    meaning text not null
);

create index on meanings (definition_id, id);

create or replace function quote_like (text)
    returns text
    language SQL
    immutable strict
    as $func$
    select
        replace(replace(replace($1, '\', ' \\ ')
        ,' _ ', ' _ ')
        ,' % ', ' % ');

$func$;

