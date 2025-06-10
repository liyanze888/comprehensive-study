package codes

import (
	"encoding/json"
	"github.com/typesense/typesense-go/typesense/api"
	"github.com/typesense/typesense-go/typesense/api/pointer"
	"log/slog"
	"strings"
	"time"
)

/**
用户和oc的交互操作 ， 在user character interaction ， 如果character 修改则进行 异步进行更新操作
*/

type (
	UserCharacterInteraction struct {
		worker *TypeSenseWorker
	}
)

const (
	UserInteraction = "user_interaction_character"
	CharacterIndex  = "character_index"
)

func NewUserCharacterInteraction(worker *TypeSenseWorker) *UserCharacterInteraction {
	return &UserCharacterInteraction{
		worker: worker,
	}
}

func (u *UserCharacterInteraction) DoWork() {
	//u.worker.DeleteIndex(UserInteraction)
	//u.worker.DeleteIndex(CharacterIndex)
	//u.CreateCharacterIndex()
	//u.CreateUserFavoriteCharacterIndex()
	//u.AddDataToCharacterIndex()
	//u.AddDataToUserFavorite()
	//u.SearchCharacter()
	u.TestAddUserFavoriteToList()
}

type (
	Character struct {
		Id          string `json:"id"`
		SId         int64  `json:"s_id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		CreatedAt   int64  `json:"created_at"`
	}
)

func (u *UserCharacterInteraction) CreateCharacterIndex() {
	var fields = []api.Field{
		{
			Name: "id", Type: "string", Index: pointer.True(),
		},
		{
			Name: "s_id", Type: "int64", Index: pointer.True(),
		},
		{
			Name: "name", Type: "string", Index: pointer.True(), Infix: pointer.True(),
		},
		{
			Name: "description", Type: "string", Index: pointer.True(), Infix: pointer.True(),
		},
		{
			Name: "created_at", Type: "int64", Sort: pointer.True(),
		},
	}
	createIndexRequest := &api.CollectionSchema{
		Name: CharacterIndex,
		//DefaultSortingField: pointer.String(entity.BasePromptFieldNameMessageCount),
		Fields: fields,
	}
	u.worker.CreateIndex(createIndexRequest)
}

type (
	UserFavoriteCharacter struct {
		Id            string  `json:"id"`
		UserId        int64   `json:"user_id"`
		CharacterId   int64   `json:"character_id"`
		CreatedAt     int64   `json:"created_at"`
		Content       string  `json:"content"`
		CharacterName string  `json:"character_name"`
		Interaction   []int64 `json:"interaction"` //用过来存储 交互的内容， 比如 点赞，收藏， 以及之后操作
		Folders       []int64 `json:"folders"`     //属于哪些 文件夹
		IsCreator     bool    `json:"is_creator"`  // 是否是创建人 用来展
		Level         int64   `json:"level"`       // 等级
	}
)

func (u *UserCharacterInteraction) CreateUserFavoriteCharacterIndex() {
	var fields = []api.Field{
		{
			Name: "id", Type: "string",
		},
		{
			Name: "user_id", Type: "int64", Index: pointer.True(),
		},
		{
			Name: "character_id", Type: "int64", Index: pointer.True(), Reference: pointer.String("character_index.s_id"),
		},
		{
			Name: "created_at", Type: "int64", Sort: pointer.True(),
		},
		{
			Name: "content", Type: "string", Index: pointer.True(), Infix: pointer.True(),
		},
		{
			Name: "character_name", Type: "string", Index: pointer.True(), Infix: pointer.True(),
		},
		{
			Name: "interaction", Type: "int64[]", Index: pointer.True(), Facet: pointer.True(), //分切面
		},
	}
	createIndexRequest := &api.CollectionSchema{
		Name: UserInteraction,
		//DefaultSortingField: pointer.String(entity.BasePromptFieldNameMessageCount),
		Fields: fields,
	}
	u.worker.CreateIndex(createIndexRequest)
	return
}

func (u *UserCharacterInteraction) TestAddUserFavoriteToList() {
	params := map[string]interface{}{
		"id": "1",
		"interaction": map[string]interface{}{
			"add": []int64{3, 4},
		},
	}
	marshal, err := json.Marshal(params)
	if err != nil {
		panic(err)
	}
	slog.Info("AddDataToUserFavorite log", slog.Any("marshal", string(marshal)))
	u.worker.UpdateData(UserInteraction, string(marshal))
}

func (u *UserCharacterInteraction) AddDataToUserFavorite() {
	var UserFavoriteCharacters = []UserFavoriteCharacter{
		{
			Id:            "1",
			UserId:        1,
			CharacterId:   1,
			CreatedAt:     time.Now().UnixMilli(),
			CharacterName: "test1",
			Content:       "1-1",
			Interaction:   []int64{1, 2},
		},
		{
			Id:            "2",
			UserId:        1,
			CharacterId:   2,
			CreatedAt:     time.Now().UnixMilli(),
			Content:       "1-2",
			CharacterName: "test2",
			Interaction:   []int64{1, 2},
		},
		{
			Id:            "3",
			UserId:        2,
			CharacterId:   1,
			CreatedAt:     time.Now().UnixMilli(),
			Content:       "2-1",
			CharacterName: "test1",
			Interaction:   []int64{1, 2},
		},
		{
			Id:            "4",
			UserId:        2,
			CharacterId:   2,
			CreatedAt:     time.Now().UnixMilli(),
			Content:       "2-2",
			CharacterName: "test2",
			Interaction:   []int64{1, 2},
		},
	}
	var (
		bulkRequestBody strings.Builder
	)
	for _, entry := range UserFavoriteCharacters {
		if err := json.NewEncoder(&bulkRequestBody).Encode(entry); err != nil {
			slog.Error("AddDataToUserFavorite error", err, slog.Any("entry", entry))
			continue
		}
	}
	slog.Info("AddDataToUserFavorite log", slog.Any("UserFavoriteCharacters", UserFavoriteCharacters))
	u.worker.UpdateData(UserInteraction, bulkRequestBody.String())
}

func (u *UserCharacterInteraction) AddDataToCharacterIndex() {
	Characters := []Character{
		{
			Id:          "1",
			SId:         1,
			Name:        "test1",
			Description: "test1",
			CreatedAt:   time.Now().UnixMilli(),
		},
		{
			Id:          "2",
			SId:         2,
			Name:        "test2",
			Description: "test2",
			CreatedAt:   time.Now().UnixMilli() + 100,
		},
	}
	var (
		bulkRequestBody strings.Builder
	)
	for _, entry := range Characters {
		if err := json.NewEncoder(&bulkRequestBody).Encode(entry); err != nil {
			slog.Error("AddDataToUserFavorite error", err, slog.Any("entry", entry))
			continue
		}
	}
	slog.Info("AddDataToCharacterIndex log", slog.Any("UserFavoriteCharacters", Characters))
	u.worker.UpdateData(CharacterIndex, bulkRequestBody.String())
}

func (u *UserCharacterInteraction) SearchCharacter() {
	searchRequest := &api.SearchCollectionParams{
		Q:             "test1",
		QueryBy:       "character_name",
		FilterBy:      pointer.String("user_id:=1"),
		IncludeFields: pointer.String("$character_index(*)"),
		SortBy:        pointer.String("$character_index(created_at:desc)"),
	}
	marshal, err := json.Marshal(searchRequest)
	if err != nil {
		panic(err)
	}
	slog.Info("marshal", slog.Any("marshal", string(marshal)))
	u.worker.Search(string(marshal), UserInteraction)
}
